package verifier

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// fakeModel devolve uma resposta combinada e guarda o que recebeu.
//
// Guardar as mensagens é o que permite provar o que NÃO vai no pedido — a
// instrução do sistema da tarefa, e o texto acima do limite de excerto. Um duplo
// que só devolvesse a resposta deixaria essas duas regressões invisíveis.
type fakeModel struct {
	completion ports.Completion
	err        error
	messages   []domain.Message
	specs      []ports.ToolSpec
}

// Complete registra a chamada e devolve o que foi combinado.
func (f *fakeModel) Complete(
	_ context.Context,
	messages []domain.Message,
	specs []ports.ToolSpec,
) (*ports.Completion, error) {
	f.messages = messages
	f.specs = specs
	if f.err != nil {
		return nil, f.err
	}
	completion := f.completion
	return &completion, nil
}

// verdictCall monta a chamada de ferramenta que o modelo faria.
func verdictCall(arguments string) []domain.ToolCall {
	return []domain.ToolCall{{ID: "c1", Name: reportVerdictTool, Arguments: arguments}}
}

// TestVerifyReadsTheStructuredVerdict cobre o caminho principal.
func TestVerifyReadsTheStructuredVerdict(t *testing.T) {
	model := &fakeModel{completion: ports.Completion{
		ToolCalls: verdictCall(`{"met":true,"evidence":"o arquivo aparece no turno 3"}`),
	}}
	verdict, err := New(model).Verify(context.Background(), "grave o arquivo", nil)
	if err != nil {
		t.Fatalf("Verify falhou: %v", err)
	}
	if !verdict.Met {
		t.Error("veredicto devia ser positivo")
	}
	if verdict.Evidence != "o arquivo aparece no turno 3" {
		t.Errorf("evidência = %q", verdict.Evidence)
	}
}

// TestVerifyOffersOnlyTheVerdictTool trava o contrato com o modelo.
//
// Oferecer qualquer outra ferramenta abriria a porta para o verificador AGIR —
// e um verificador que executa é um segundo agente, com o mesmo problema de
// parar sem cumprir. A regressão começaria aí.
func TestVerifyOffersOnlyTheVerdictTool(t *testing.T) {
	model := &fakeModel{completion: ports.Completion{
		ToolCalls: verdictCall(`{"met":true,"evidence":"x"}`),
	}}
	if _, err := New(model).Verify(context.Background(), "faça algo", nil); err != nil {
		t.Fatalf("Verify falhou: %v", err)
	}
	if len(model.specs) != 1 || model.specs[0].Name != reportVerdictTool {
		t.Errorf("ferramentas oferecidas: %+v, want só %s", model.specs, reportVerdictTool)
	}
}

// TestVerifyTreatsMissingToolCallAsError separa "não verificou" de "reprovou".
//
// Modelo que não chama a ferramenta significa que a verificação NÃO aconteceu.
// Traduzir isso em veredicto negativo bloquearia a tarefa por um defeito do
// verificador, e quem olhasse a tela leria "não cumpriu" sobre trabalho que pode
// estar perfeito.
func TestVerifyTreatsMissingToolCallAsError(t *testing.T) {
	model := &fakeModel{completion: ports.Completion{
		Content:    "acho que está tudo certo",
		StopReason: "stop",
	}}
	verdict, err := New(model).Verify(context.Background(), "faça algo", nil)
	if err == nil {
		t.Fatal("resposta sem chamada de ferramenta devia virar erro")
	}
	if verdict.Met {
		t.Error("veredicto de erro não pode vir marcado como cumprido")
	}
}

// TestVerifyPropagatesModelFailure garante que falha de rede vira erro.
func TestVerifyPropagatesModelFailure(t *testing.T) {
	model := &fakeModel{err: errors.New("timeout")}
	if _, err := New(model).Verify(context.Background(), "faça algo", nil); err == nil {
		t.Fatal("erro do modelo devia ser propagado")
	}
}

// TestVerifyRejectsUnreadableArguments cobre JSON malformado.
//
// O fornecedor valida o schema, mas o adaptador não pode confiar nisso a ponto
// de entrar em pânico: argumento ilegível é erro tratado, não queda do laço.
func TestVerifyRejectsUnreadableArguments(t *testing.T) {
	model := &fakeModel{completion: ports.Completion{
		ToolCalls: verdictCall(`{"met":true,`),
	}}
	if _, err := New(model).Verify(context.Background(), "faça algo", nil); err == nil {
		t.Fatal("JSON quebrado devia virar erro")
	}
}

// TestVerifyFillsMissingWhenVerdictIsNegative cobre a recusa sem detalhe.
//
// Recusa sem dizer o que falta é inútil para quem recebe: a devolução ao modelo
// ficaria "falta: " e o bloqueio não diria à pessoa o que fazer.
func TestVerifyFillsMissingWhenVerdictIsNegative(t *testing.T) {
	model := &fakeModel{completion: ports.Completion{
		ToolCalls: verdictCall(`{"met":false,"evidence":"nada no histórico"}`),
	}}
	verdict, err := New(model).Verify(context.Background(), "faça algo", nil)
	if err != nil {
		t.Fatalf("Verify falhou: %v", err)
	}
	if strings.TrimSpace(verdict.Missing) == "" {
		t.Error("recusa sem detalhe devia receber um texto padrão")
	}
}

// TestQuestionCarriesRequestAndHistory prova o que entra no pedido.
func TestQuestionCarriesRequestAndHistory(t *testing.T) {
	history := []domain.Message{
		{Role: domain.RoleSystem, Content: "INSTRUÇÃO DA TAREFA que não deve vazar"},
		{Role: domain.RoleUser, Content: "grave o arquivo"},
		{Role: domain.RoleAssistant, Content: "gravei em /tmp/saida"},
	}
	question := buildQuestion("o pedido original", history)

	if !strings.Contains(question, "o pedido original") {
		t.Error("o pedido não entrou na pergunta")
	}
	if !strings.Contains(question, "gravei em /tmp/saida") {
		t.Error("o histórico não entrou na pergunta")
	}
	// A instrução do sistema da tarefa não é trabalho feito, e ocupa o espaço
	// que a evidência precisa.
	if strings.Contains(question, "INSTRUÇÃO DA TAREFA") {
		t.Error("a instrução do sistema vazou para a verificação")
	}
}

// TestTruncateMarksTheCut cobre o corte de mensagem longa.
//
// A marca importa: sem ela o verificador leria um comando pela metade como se
// fosse o comando inteiro, e poderia concluir que algo não foi feito porque a
// prova ficou depois do corte.
func TestTruncateMarksTheCut(t *testing.T) {
	short := "curto"
	if got := truncate(short, maxExcerpt); got != short {
		t.Errorf("texto curto foi alterado: %q", got)
	}

	long := strings.Repeat("x", maxExcerpt+50)
	cut := truncate(long, maxExcerpt)
	if !strings.Contains(cut, "caracteres]") {
		t.Error("o corte não foi marcado")
	}
	if len([]rune(cut)) >= len([]rune(long)) {
		t.Error("o texto não encurtou")
	}
}
