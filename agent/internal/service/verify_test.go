package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// stubVerifier devolve veredictos combinados, um por chamada.
//
// Fila e não valor fixo porque o caminho que interessa tem MAIS DE UMA
// verificação: a primeira recusa, o modelo corrige, a segunda aprova. Um duplo
// que respondesse sempre a mesma coisa não conseguiria exercitar isso.
type stubVerifier struct {
	verdicts []Verdict
	err      error
	calls    int
	requests []string
}

// Verify devolve o próximo veredicto da fila e anota o pedido recebido.
//
// Guardar o pedido é o que permite provar que ele vem de `task.Prompt`, e não da
// conversa podada — a regressão que `Trim` e `Compact` poderiam introduzir sem
// nenhum sintoma visível.
func (s *stubVerifier) Verify(_ context.Context, request string, _ []domain.Message) (Verdict, error) {
	s.calls++
	s.requests = append(s.requests, request)
	if s.err != nil {
		return Verdict{}, s.err
	}
	if len(s.verdicts) == 0 {
		return Verdict{Met: true}, nil
	}
	next := s.verdicts[0]
	if len(s.verdicts) > 1 {
		s.verdicts = s.verdicts[1:]
	}
	return next, nil
}

// TestDiscardVerifierApprovesDefault trava o default do verificador.
//
// Garante que ligar a verificação seja uma escolha: quem só atualizou o binário
// não pode ver a tarefa parar de terminar na primeira parada, nem pagar uma
// chamada de modelo a mais por tarefa.
func TestDiscardVerifierApprovesDefault(t *testing.T) {
	verdict, err := discardVerifier{}.Verify(context.Background(), "qualquer", nil)
	if err != nil {
		t.Fatalf("o verificador mudo devolveu erro: %v", err)
	}
	if !verdict.Met {
		t.Error("o verificador mudo reprovou — o default tem de aprovar")
	}
}

// TestVerifyAttemptsReadsEnvironment cobre o teto ajustável e seus ramos.
//
// Ajustável porque teto que só muda recompilando é teto desligado — e porque o
// teste de contenção precisa forçar o bloqueio sem depender de o modelo
// cooperar em falhar, armadilha já registrada no README.
func TestVerifyAttemptsReadsEnvironment(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int
	}{
		{"sem variável usa o padrão", "", defaultVerifyAttempts},
		{"valor válido é respeitado", "5", 5},
		{"zero desliga a devolução", "0", 0},
		{"texto inválido cai no padrão", "muitas", defaultVerifyAttempts},
		{"negativo cai no padrão", "-3", defaultVerifyAttempts},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("AGENTD_MAX_VERIFY_ATTEMPTS", testCase.value)
			if got := verifyAttempts(); got != testCase.want {
				t.Errorf("verifyAttempts() = %d, want %d", got, testCase.want)
			}
		})
	}
}

// TestVerifyLessonStatesTheGap prova que a devolução é acionável.
//
// A mensagem vira contexto das próximas iterações. Se ela acusasse erro em vez
// de dizer o que falta, o turno seguinte seria gasto com o modelo se
// justificando — por isso o teste exige o texto do que falta e a instrução de
// continuar, não de recomeçar.
func TestVerifyLessonStatesTheGap(t *testing.T) {
	lesson := verifyLesson(Verdict{Missing: "gravar o resultado em /tmp/saida"}, 1, 2)
	for _, required := range []string{"gravar o resultado em /tmp/saida", "1 de 2", "Não recomece"} {
		if !strings.Contains(lesson, required) {
			t.Errorf("a lição não menciona %q:\n%s", required, lesson)
		}
	}
}

// TestBlockUnverifiedReasonIsValid liga o motivo novo à validação do domínio.
//
// Sem isto o bloqueio seria recusado por ValidBlockReason e a tarefa terminaria
// pelo caminho de erro — falha que só apareceria no primeiro veredicto negativo
// em produção.
func TestBlockUnverifiedReasonIsValid(t *testing.T) {
	if !domain.ValidBlockReason(domain.BlockUnverified) {
		t.Error("BlockUnverified não é aceito por ValidBlockReason")
	}
}

// TestVerifierErrorIsAbstention trava o comportamento diante de falha.
//
// Verificação indisponível NÃO pode reprovar: tratar erro como "não cumpriu"
// faria uma falha de rede transformar toda tarefa boa em bloqueio, e isso só
// apareceria quando o backend já estivesse fora do ar — o pior momento.
func TestVerifierErrorIsAbstention(t *testing.T) {
	verifier := &stubVerifier{err: errors.New("backend fora do ar")}
	verdict, err := verifier.Verify(context.Background(), "faça algo", nil)
	if err == nil {
		t.Fatal("o duplo devia ter devolvido erro")
	}
	if verdict.Met {
		t.Error("veredicto com erro não pode vir marcado como cumprido")
	}
}

// TestStubVerifierWalksTheQueue prova que o duplo serve ao caminho de duas
// verificações — a primeira recusa, a segunda aprova.
//
// Testar o próprio duplo evita o modo de falha em que o teste do laço passa por
// um motivo errado: se a fila não avançasse, a segunda verificação repetiria a
// recusa e o teste principal mediria outra coisa.
func TestStubVerifierWalksTheQueue(t *testing.T) {
	verifier := &stubVerifier{verdicts: []Verdict{
		{Met: false, Missing: "falta gravar"},
		{Met: true, Evidence: "gravado"},
	}}
	first, _ := verifier.Verify(context.Background(), "x", nil)
	second, _ := verifier.Verify(context.Background(), "x", nil)
	if first.Met {
		t.Error("a primeira devia recusar")
	}
	if !second.Met {
		t.Error("a segunda devia aprovar")
	}
	if verifier.calls != 2 {
		t.Errorf("chamadas = %d, want 2", verifier.calls)
	}
}
