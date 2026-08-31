package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// Limites dos detectores. Cada um com o número medido ao lado — constante sem
// origem vira número mágico que ninguém ousa mudar depois.
const (
	// maxTurnsPerTask é o teto ACUMULADO, atravessando retomadas.
	//
	// 180 = três invocações cheias de `maxIterations` (60). O teto por invocação
	// continua existindo e é o que pega laço curto; este pega o padrão que ele
	// não via: bloquear, retomar, bloquear, retomar, indefinidamente.
	maxTurnsPerTask = 180

	// maxIdenticalToolFailures é quantas falhas IDÊNTICAS seguidas param a tarefa.
	//
	// Três, e não duas: a segunda tentativa é comportamento legítimo e comum —
	// o modelo corrige um caminho, tenta de novo e acerta. A terceira repetição
	// exata do mesmo par (ferramenta, argumentos) não é tentativa, é laço.
	maxIdenticalToolFailures = 3

	// wallClockWarnFraction é a fração do tempo da tarefa em que se bloqueia.
	//
	// 0,8 de 2 h = 1h36. O que sobra é margem para gravar estado, avisar e
	// soltar a tela antes de o contexto ser cancelado — o corte seco vira
	// `failed` e perde o trabalho junto.
	wallClockWarnFraction = 0.8

	// maxGuardrailsBytes limita o que é injetado no prompt.
	//
	// O arquivo entra em TODA iteração de TODA tarefa. Sem teto ele cresce para
	// sempre e passa a custar mais do que evita; ao estourar, a lição mais
	// antiga é descartada.
	maxGuardrailsBytes = 4096
)

// Os limiares efetivos, lidos do ambiente na subida.
//
// Existem por dois motivos, e o segundo é o que justifica o código extra:
//
//  1. OPERAÇÃO. Ajustar um teto que se mostrou apertado não deveria exigir
//     recompilar e reinstalar o binário.
//  2. TESTE NA MÁQUINA. Provar que o bloqueio acontece de verdade exige forçar
//     o limiar, e forçá-lo pedindo ao modelo que insista não funciona — ele é
//     sensato e desiste antes. Medido em 30/08/2026: pedindo repetição
//     explícita, ele repetiu DUAS vezes e parou, deixando o detector a um passo
//     de disparar.
//
// Sem a variável, valem as constantes acima. Valor inválido é ignorado em
// silêncio de propósito: um teto malformado não pode derrubar o agente, e cair
// no padrão é o comportamento seguro.
var (
	turnCap           = envInt("AGENTD_MAX_TURNS", maxTurnsPerTask)
	identicalFailures = envInt("AGENTD_MAX_TOOL_FAILURES", maxIdenticalToolFailures)
)

// envInt lê um inteiro positivo do ambiente, ou devolve o padrão.
func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// GuardrailKind identifica qual detector disparou.
type GuardrailKind string

const (
	GuardrailTurnCap   GuardrailKind = "teto-de-turnos"
	GuardrailToolLoop  GuardrailKind = "ferramenta-em-laco"
	GuardrailWallClock GuardrailKind = "tempo-de-parede"
	GuardrailTruncated GuardrailKind = "resposta-truncada"
)

// GuardrailHit é o veredito de um detector.
type GuardrailHit struct {
	Kind GuardrailKind
	// Detail é o que a pessoa lê na tela. Precisa dizer o que houve E o que
	// fazer — "limite atingido" sozinho não ajuda ninguém.
	Detail string
	// Lesson é a linha que vai para `guardrails.md` e, dali, para o prompt das
	// próximas tarefas. Vazia quando o caso não ensina nada reaproveitável.
	Lesson string
}

// GuardrailJournal escreve os quatro arquivos de memória.
//
// É porto, e não implementação direta, por um motivo prático: o laço precisa
// ser testável sem disco, e um dublê aqui é a diferença entre teste de tabela
// rápido e teste que monta diretório temporário.
type GuardrailJournal interface {
	// RecordActivity anota uma linha de atividade (iteração, ferramenta, tempo).
	RecordActivity(ctx context.Context, line string) error
	// RecordError anota uma falha de ferramenta com a contagem de repetição.
	RecordError(ctx context.Context, line string) error
	// RecordProgress anota o desfecho de uma tarefa.
	RecordProgress(ctx context.Context, line string) error
	// LearnLesson grava uma lição que passará a entrar no prompt.
	LearnLesson(ctx context.Context, kind GuardrailKind, lesson string) error
	// Lessons devolve o que foi aprendido, para entrar no prompt.
	//
	// Ler faz parte do MESMO porto que escreve, e não de um separado, porque é
	// o par que dá sentido ao mecanismo: no ralph a lição é gravada e nunca
	// lida, e o sistema inteiro parece funcionar. Interfaces separadas
	// permitiriam ligar só metade e não notar.
	Lessons() (string, error)
}

// guardrailState acompanha UMA tarefa em execução.
//
// Vive fora do `domain.Task` de propósito: o que interessa persistir é o
// contador de turnos (que precisa sobreviver à retomada); a contagem de falhas
// repetidas é intracorrida — um laço que atravessa uma intervenção humana não
// é o mesmo laço.
type guardrailState struct {
	mu sync.Mutex
	// lastFailureKey identifica a última falha por (ferramenta, argumentos).
	lastFailureKey string
	// repeatCount conta quantas vezes seguidas a MESMA falha ocorreu.
	repeatCount int
	// startedAt marca o começo desta invocação, para o detector de tempo.
	startedAt time.Time
	// budget é o tempo total concedido à tarefa; zero desliga o detector.
	budget time.Duration
}

// newGuardrailState cria o acompanhamento de uma invocação.
func newGuardrailState(now time.Time, budget time.Duration) *guardrailState {
	return &guardrailState{startedAt: now, budget: budget}
}

// failureKey resume (ferramenta, argumentos) num identificador estável.
//
// Hash, e não o texto cru: argumentos de ferramenta chegam a quilobytes, e
// guardar o original só para comparar igualdade seria carregar o histórico
// inteiro de novo. Colisão de SHA-256 aqui não tem consequência de segurança —
// no pior caso o detector conta duas falhas diferentes como iguais.
func failureKey(name, arguments string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + arguments))
	return name + ":" + hex.EncodeToString(sum[:8])
}

// observeToolFailure registra uma falha e diz se ela virou laço.
//
// Sequência QUEBRADA por qualquer sucesso ou por falha diferente: o contador
// zera. É o que separa "insiste no mesmo erro" de "erra em coisas variadas
// enquanto explora" — o segundo é trabalho normal.
func (g *guardrailState) observeToolFailure(name, arguments string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := failureKey(name, arguments)
	if key != g.lastFailureKey {
		g.lastFailureKey = key
		g.repeatCount = 1
		return 1
	}
	g.repeatCount++
	return g.repeatCount
}

// observeToolSuccess zera a sequência.
func (g *guardrailState) observeToolSuccess() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastFailureKey = ""
	g.repeatCount = 0
}

// checkTurnCap testa o teto acumulado de turnos.
func checkTurnCap(task *domain.Task) *GuardrailHit {
	if task.TurnsUsed < turnCap {
		return nil
	}
	return &GuardrailHit{
		Kind: GuardrailTurnCap,
		Detail: fmt.Sprintf(
			"a tarefa usou %d turnos de modelo (teto %d, somando as retomadas) e parou. "+
				"Confira o que ela estava tentando e retome só se o caminho tiver mudado.",
			task.TurnsUsed, turnCap),
		Lesson: "",
	}
}

// checkToolLoop testa a repetição de falha idêntica.
func checkToolLoop(name, arguments, errText string, repeats int) *GuardrailHit {
	if repeats < identicalFailures {
		return nil
	}
	// O erro literal vai no detalhe: sem ele a pessoa abre a conversa inteira
	// para descobrir o que falhou, e o ponto do bloqueio é justamente poupar isso.
	trimmed := strings.TrimSpace(errText)
	if len(trimmed) > 240 {
		trimmed = trimmed[:240] + "…"
	}
	return &GuardrailHit{
		Kind: GuardrailToolLoop,
		Detail: fmt.Sprintf(
			"a ferramenta %s falhou %d vezes seguidas com os mesmos argumentos: %s",
			name, repeats, trimmed),
		Lesson: fmt.Sprintf(
			"A ferramenta %s falhou %d vezes com argumentos idênticos (%s). "+
				"Repetir a mesma chamada não muda o resultado — mude os argumentos ou o caminho.",
			name, repeats, trimmed),
	}
}

// checkWallClock testa a fração do tempo consumida.
//
// Devolve nil quando não há orçamento definido: pelo CLI não existe teto de
// tempo, e inventar um aqui mudaria o comportamento de um caminho que ninguém
// pediu para mudar.
func (g *guardrailState) checkWallClock(now time.Time) *GuardrailHit {
	if g.budget <= 0 {
		return nil
	}
	limit := time.Duration(float64(g.budget) * wallClockWarnFraction)
	elapsed := now.Sub(g.startedAt)
	if elapsed < limit {
		return nil
	}
	return &GuardrailHit{
		Kind: GuardrailWallClock,
		Detail: fmt.Sprintf(
			"a tarefa passou de %s (%.0f%% do tempo concedido, %s) e parou antes do corte. "+
				"O trabalho está gravado; retome se ainda fizer sentido.",
			elapsed.Round(time.Minute), wallClockWarnFraction*100, g.budget),
		Lesson: "",
	}
}

// applyHit bloqueia a tarefa e registra a lição.
//
// O bloqueio usa a MESMA máquina do take-over — `task.Block`, aviso na tela,
// evento na fila. Não há caminho paralelo: um detector que encerrasse a tarefa
// de outro jeito teria de reimplementar a preservação de estado que o take-over
// já faz certo, e as duas cópias divergiriam.
func (a *Agent) applyHit(ctx context.Context, task *domain.Task, conv *domain.Conversation, hit *GuardrailHit) error {
	if err := task.Block(domain.BlockGuardrail, hit.Detail, a.clock()); err != nil {
		// Transição inválida aqui não pode virar silêncio: se a tarefa já saiu
		// de `running`, o detector chegou tarde e é isso que precisa aparecer.
		return fmt.Errorf("guardrail %s não conseguiu bloquear: %w", hit.Kind, err)
	}

	// A lição é gravada ANTES do aviso: se o processo morrer no meio, é melhor
	// ter a lição sem o aviso do que o inverso — a lição serve às próximas
	// tarefas, o aviso serve a esta.
	if hit.Lesson != "" {
		if err := a.journal.LearnLesson(ctx, hit.Kind, hit.Lesson); err != nil {
			// Falha ao aprender não derruba o bloqueio: a contenção já
			// aconteceu, e perder a lição é menos grave que perder a parada.
			_ = conv.AddSystemNote(fmt.Sprintf("não consegui gravar a lição: %v", err))
		}
	}
	_ = a.journal.RecordError(ctx, fmt.Sprintf("guardrail=%s tarefa=%s tela=%d %s",
		hit.Kind, task.ID, task.Screen, hit.Detail))

	_ = a.screen.RequestTakeover(ctx, task.Screen, domain.BlockGuardrail, hit.Detail)
	_ = a.screen.ShowStatus(ctx, task.Screen, task.StatusLine())
	return nil
}

// guardrailToolResultFailed diz se o resultado de uma ferramenta é falha.
//
// Existe como função nomeada porque o campo `Failed` era escrito por TODAS as
// ferramentas e lido por NENHUM consumidor — medido em 30/08/2026, com grep em
// `internal/service/` voltando vazio. Um comando de shell que falhava entrava
// no histórico pelo ramo de sucesso, e o laço nunca sabia. Sem ler isto, não há
// dado para detectar repetição.
func guardrailToolResultFailed(result *ports.ToolResult, err error) bool {
	if err != nil {
		return true
	}
	return result != nil && result.Failed
}

// truncatedStop diz se o modelo parou por limite de saída, e não por ter
// terminado.
//
// Os fornecedores divergem no rótulo: a OpenAI e compatíveis usam "length", a
// Anthropic usa "max_tokens". Aceitar os dois evita que a troca de fornecedor
// reabra o defeito em silêncio — e o custo de aceitar um rótulo a mais é zero.
func truncatedStop(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	}
	return false
}

// observeOutcome alimenta o detector de laço com o resultado de uma ferramenta.
//
// Devolve o veredito quando a MESMA falha se repetiu além do teto. Sucesso zera
// a sequência, e é isso que impede o detector de somar erros esparsos de uma
// exploração legítima.
func (a *Agent) observeOutcome(ctx context.Context, guard *guardrailState, out toolOutcome) *GuardrailHit {
	// Ferramenta desconhecida não entra na conta: o modelo inventou um nome, o
	// histórico já diz isso a ele, e tratar como falha de execução misturaria
	// dois defeitos diferentes na mesma métrica.
	if !out.known {
		return nil
	}
	if !guardrailToolResultFailed(out.result, out.err) {
		guard.observeToolSuccess()
		return nil
	}

	errText := ""
	switch {
	case out.err != nil:
		errText = out.err.Error()
	case out.result != nil:
		errText = out.result.Output
	}

	repeats := guard.observeToolFailure(out.call.Name, out.call.Arguments)
	_ = a.journal.RecordError(ctx, fmt.Sprintf("ferramenta=%s repeticao=%d %s",
		out.call.Name, repeats, strings.ReplaceAll(strings.TrimSpace(errText), "\n", " ")))
	return checkToolLoop(out.call.Name, out.call.Arguments, errText, repeats)
}

// recordTurn anota a iteração no diário de atividade.
//
// É a observabilidade que o laço não tinha: `service.Agent` não recebia logger
// nenhum, e a única forma de saber em que ponto uma tarefa estava era contar
// mensagens no JSON da conversa.
//
// Os tokens entram aqui porque os campos já vinham preenchidos pelo adaptador e
// não eram lidos por ninguém. Isto ainda NÃO é teto de custo — é o registro que
// torna um teto possível depois, com número medido em vez de estimado.
func (a *Agent) recordTurn(ctx context.Context, task *domain.Task, iteration int, completion *ports.Completion, elapsed time.Duration) {
	tools := make([]string, 0, len(completion.ToolCalls))
	for _, call := range completion.ToolCalls {
		tools = append(tools, call.Name)
	}
	toolNames := "nenhuma"
	if len(tools) > 0 {
		toolNames = strings.Join(tools, ",")
	}
	_ = a.journal.RecordActivity(ctx, fmt.Sprintf(
		"tarefa=%s tela=%d iteracao=%d turnos=%d duracao=%s tokens=%d/%d parada=%s ferramentas=%s",
		task.ID, task.Screen, iteration+1, task.TurnsUsed,
		elapsed.Round(time.Millisecond),
		completion.PromptTokens, completion.CompletionTokens,
		completion.StopReason, toolNames))
}

// systemPromptWithLessons devolve a instrução de sistema com as lições anexadas.
//
// AQUI está a diferença central em relação ao ralph. Lá o prompt recebe o
// CAMINHO do arquivo (`{{GUARDRAILS_PATH}}`) e um pedido educado para o modelo
// lê-lo; se ele não ler, nada acontece, e nada verifica que leu. Aqui o serviço
// lê e concatena — a lição chega mesmo que o modelo nunca faça uma chamada de
// leitura, porque não depende dele.
//
// Vai no prompt de SISTEMA, e não no pedido: é instrução permanente, e misturá-la
// à tarefa faria o modelo tratá-la como parte do objetivo.
//
// Falha de leitura devolve a instrução original: ficar sem lição é pior que a
// tarefa não rodar, mas é MUITO melhor que derrubar a tarefa por causa do
// arquivo de memória.
func (a *Agent) systemPromptWithLessons() string {
	lessons, err := a.journal.Lessons()
	if err != nil || strings.TrimSpace(lessons) == "" {
		return a.sysPrompt
	}
	return a.sysPrompt + "\n\n--- lições de execuções anteriores ---\n" +
		"Estas linhas vêm de limites que já foram atingidos nesta máquina. " +
		"Não são sugestões: repetir o que está aqui volta a parar a tarefa.\n" +
		lessons + "\n--- fim das lições ---"
}
