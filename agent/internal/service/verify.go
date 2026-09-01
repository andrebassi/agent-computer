package service

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// # Por que a tarefa precisa de uma verificação separada
//
// Até 01/09/2026 o laço concluía a tarefa quando o modelo parava de chamar
// ferramentas (`agent.go`, ramo `len(completion.ToolCalls) == 0`). Isso mede que
// ele PAROU, não que cumpriu — são coisas diferentes, e a diferença é onde mora
// o defeito silencioso: a tarefa fica `done`, ninguém olha, e o que foi pedido
// não aconteceu.
//
// O repositório já tinha um caso particular disto resolvido: `GuardrailTruncated`
// nasceu porque uma resposta cortada por limite de saída chega SEM chamada de
// ferramenta e era tratada como conclusão. O comentário de lá é literal — "a
// tarefa terminava done, com a resposta pela metade e ninguém sabendo". Esta
// verificação é a generalização daquele achado: truncamento é uma das formas de
// parar sem cumprir, e não a única.
//
// # O que ela NÃO é
//
// Não é um segundo agente, nem uma suíte de testes. É uma pergunta fechada sobre
// o histórico que já existe: o que foi pedido foi feito, e qual a evidência
// disso no próprio histórico. Um verificador que pudesse EXECUTAR coisas seria
// outro agente, com o mesmo problema de parar sem cumprir — e a regressão
// infinita começaria aí.

// Verdict é o que o verificador responde sobre uma tarefa que parou.
type Verdict struct {
	// Met diz se o que foi pedido foi cumprido.
	Met bool

	// Evidence é o trecho do histórico que sustenta o veredicto.
	//
	// Exigir evidência é o que separa verificação de opinião. Um verificador
	// que respondesse só `Met` tenderia a aprovar — é o mesmo modelo julgando o
	// próprio trabalho, e concordar consigo mesmo é o caminho barato. Pedir
	// "onde no histórico isso aconteceu" torna a aprovação falsa mais cara que
	// a honesta.
	Evidence string

	// Missing descreve o que falta, quando não foi cumprido.
	//
	// Vai para o modelo como próxima instrução, então é redigido como tarefa
	// pendente e não como reprovação: "falta gravar o arquivo em /tmp/x", nunca
	// "você errou".
	Missing string
}

// CompletionVerifier julga se uma tarefa que parou realmente cumpriu o pedido.
//
// Porto LOCAL, definido aqui e não em `ports/`, seguindo o precedente de
// `GuardrailJournal` e `CostEstimator`: quem o implementa é um adaptador, mas
// quem define a pergunta é o laço. Manter a interface junto do laço é o que
// permite o default mudo abaixo sem que `ports` conheça um conceito que só o
// serviço usa.
type CompletionVerifier interface {
	// Verify recebe o pedido original e o histórico, e devolve o veredicto.
	//
	// Erro NÃO reprova a tarefa: verificação indisponível não pode ser
	// tratada como "não cumpriu", senão uma falha de rede transformaria toda
	// tarefa boa em bloqueio. Quem chama trata erro como abstenção.
	Verify(ctx context.Context, request string, history []domain.Message) (Verdict, error)
}

// discardVerifier aprova tudo sem perguntar nada.
//
// É o default, e a escolha é deliberada: ligar a verificação muda o custo (uma
// chamada de modelo por tarefa) e o comportamento (tarefa pode não terminar na
// primeira parada). Nenhuma das duas coisas pode acontecer com quem só atualizou
// o binário — o molde é o mesmo de `discardTracer` e `noCost`.
type discardVerifier struct{}

// Verify aprova sem consultar nada.
func (discardVerifier) Verify(context.Context, string, []domain.Message) (Verdict, error) {
	return Verdict{Met: true, Evidence: "verificação desligada"}, nil
}

// WithVerifier liga a verificação de conclusão.
func WithVerifier(verifier CompletionVerifier) Option {
	return func(a *Agent) {
		if verifier != nil {
			a.verifier = verifier
		}
	}
}

// defaultVerifyAttempts é quantas vezes o laço devolve a lacuna ao modelo.
//
// Dois, e não um: a primeira devolução costuma bastar para algo que o modelo
// esqueceu de fazer, e a segunda cobre o caso em que a correção depende de um
// passo intermediário. A partir daí, insistir é o padrão que o detector de
// falhas repetidas já trata como laço — mais tentativas gastariam turno para
// repetir a mesma coisa.
//
// Zero desliga a devolução: o veredicto negativo vira bloqueio direto, que é o
// que se quer quando alguém está medindo com que frequência o agente para sem
// cumprir, sem deixá-lo corrigir.
const defaultVerifyAttempts = 2

// verifyAttempts lê o teto de devoluções do ambiente.
//
// Ajustável pelo mesmo motivo dos outros tetos (docs/GUARDRAILS.md): teto que só
// muda recompilando é teto desligado. E aqui há um motivo a mais — o teste de
// contenção precisa forçar o caminho do bloqueio sem depender de o modelo
// cooperar em falhar, que é a armadilha já registrada no README.
func verifyAttempts() int {
	raw := os.Getenv("AGENTD_MAX_VERIFY_ATTEMPTS")
	if raw == "" {
		return defaultVerifyAttempts
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return defaultVerifyAttempts
	}
	return n
}

// verifyLesson monta a mensagem que devolve a lacuna ao modelo.
//
// Redigida como instrução do que falta, não como reprovação: o histórico dela
// vira contexto das próximas iterações, e um texto que acusa erro gasta turno
// com o modelo se justificando em vez de agindo.
func verifyLesson(verdict Verdict, attempt, limit int) string {
	return fmt.Sprintf(
		"A tarefa ainda não está cumprida (verificação %d de %d). Falta: %s\n"+
			"Continue de onde parou e faça o que falta. Não recomece do zero.",
		attempt, limit, verdict.Missing)
}

// applyUnverified bloqueia a tarefa que parou sem cumprir.
//
// Segue o molde de `applyHit`, com uma diferença que importa: o motivo é
// `BlockUnverified` e não `BlockGuardrail`. Reaproveitar `guardrail` faria a
// tela dizer "contivemos o agente" quando o caso é o oposto — ele entregou de
// menos —, e é na hora de decidir o que fazer que a pessoa precisa disso.
func (a *Agent) applyUnverified(
	ctx context.Context,
	task *domain.Task,
	conv *domain.Conversation,
	verdict Verdict,
) error {
	detail := fmt.Sprintf(
		"o agente parou, mas o pedido não foi cumprido depois de %d tentativa(s). Falta: %s",
		verifyAttempts(), verdict.Missing)

	if err := task.Block(domain.BlockUnverified, detail, a.clock()); err != nil {
		// Transição inválida não pode virar silêncio: se a tarefa já saiu de
		// `running`, a verificação chegou tarde e é isso que precisa aparecer.
		return fmt.Errorf("verificação não conseguiu bloquear: %w", err)
	}

	_ = a.journal.RecordError(ctx, fmt.Sprintf(
		"nao-cumprido tarefa=%s tela=%d%s %s",
		task.ID, task.Screen, a.traceSuffix(ctx), verdict.Missing))

	// A evidência entra na conversa, não no diário de erros: ela é o raciocínio
	// do verificador, e quem retomar a tarefa a lê no histórico — que é onde já
	// está olhando ao decidir o que fazer.
	if verdict.Evidence != "" {
		_ = conv.AddSystemNote(fmt.Sprintf("verificação: %s", verdict.Evidence))
	}

	// Só os enums, nunca o texto: `Missing` e `Evidence` descrevem conteúdo da
	// tarefa, e telemetria atravessa a rede enquanto o estado fica no volume.
	a.tracer.AddEvent(ctx, eventGuardrailHit,
		String(attrGuardrailKind, string(GuardrailUnverified)),
		String(attrBlockReason, string(domain.BlockUnverified)),
	)
	a.meter.AddCount(ctx, metricGuardrailHits, 1,
		String(attrGuardrailKind, string(GuardrailUnverified)))
	return nil
}
