package service

import (
	"context"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// TestLoopOpensExpectedSpans prova que uma tarefa com ferramenta produz a
// cascata inteira: a tarefa, a chamada ao modelo e a execução da ferramenta.
//
// O teste falha se qualquer um dos três pontos de instrumentação for removido —
// que é o critério do `docs/TEST-MAP.md`: um teste só cobre uma funcionalidade
// se falhar quando ela sumir.
func TestLoopOpensExpectedSpans(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{
		{
			ToolCalls: []domain.ToolCall{{ID: "c1", Name: "eco", Arguments: `{"x":1}`}},
			// O motivo de parada vira atributo do trecho da chamada.
			StopReason:       "tool_calls",
			PromptTokens:     100,
			CompletionTokens: 20,
			CachedTokens:     80,
		},
		{Content: "pronto", StopReason: "stop"},
	}}
	tool := &fakeTool{name: "eco", result: ports.ToolResult{Output: "ok"}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	spy := &spyTracer{}

	agent := NewAgent(model, []ports.Tool{tool}, screen, store, lock, fixedClock,
		"instruções", WithTracer(spy))

	task, err := domain.NewTask("task-telemetry", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	// Um trecho por ponto instrumentado. O nome da chamada ao modelo e o da
	// ferramenta carregam sufixo, como manda a convenção GenAI — por isso a
	// verificação é por prefixo.
	wants := []struct {
		prefix string
		why    string
	}{
		{spanTask, "sem ele não há tarefa no backend, só fragmentos soltos"},
		{spanChat, "sem ele não se sabe quanto do relógio foi o modelo"},
		{spanExecuteTool + " eco", "sem ele nenhuma ferramenta é cronometrada — o buraco que esta fase fecha"},
	}
	for _, want := range wants {
		if !hasPrefix(spy.started, want.prefix) {
			t.Errorf("trecho %q não foi aberto: %s\n  abertos: %v", want.prefix, want.why, spy.started)
		}
	}

	// O evento de turno carrega o acumulado, e é o que desenha a curva de custo.
	if !contains(spy.events, eventTurn) {
		t.Errorf("evento %q não foi marcado; a curva de custo por turno some\n  eventos: %v",
			eventTurn, spy.events)
	}
}

// TestTakeoverEmitsEvent prova que o pedido de ajuda aparece na telemetria.
//
// É o evento mais importante que a máquina produz — o único que exige uma
// pessoa. Sem ele, "quanto tempo a tarefa passou esperando alguém" só se
// responde abrindo a conversa à mão, por SSH.
func TestTakeoverEmitsEvent(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{
		{
			ToolCalls:  []domain.ToolCall{{ID: "c1", Name: "request_takeover", Arguments: `{}`}},
			StopReason: "tool_calls",
		},
	}}
	// A ferramenta devolve o pedido de take-over no campo que o laço lê.
	tool := &fakeTool{
		name: "request_takeover",
		result: ports.ToolResult{
			Output:       "preciso de você",
			BlockRequest: &ports.BlockRequest{Reason: domain.BlockPassword, Detail: "pediu senha"},
		},
	}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	spy := &spyTracer{}

	agent := NewAgent(model, []ports.Tool{tool}, screen, store, lock, fixedClock,
		"instruções", WithTracer(spy))

	task, err := domain.NewTask("task-takeover", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	if !contains(spy.events, eventTakeoverRequested) {
		t.Errorf("o pedido de take-over não virou evento\n  eventos: %v", spy.events)
	}
}

// TestJournalCarriesTraceID prova o elo entre o arquivo e o backend.
//
// É o que transforma o `activity.log` de "a única observabilidade do laço" em
// porta de entrada: quem estiver lendo o arquivo por SSH copia o id e abre a
// cascata inteira, sem correlacionar por horário — que é o método que erra
// quando duas tarefas rodam em telas diferentes ao mesmo tempo.
func TestJournalCarriesTraceID(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	journal := &recordingJournal{}
	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções",
		WithGuardrailJournal(journal), WithTracer(&spyTracer{}))

	task, err := domain.NewTask("task-journal", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	if len(journal.activity) == 0 {
		t.Fatal("nenhuma linha de atividade foi escrita")
	}
	line := journal.activity[0]
	if !strings.Contains(line, "trace_id="+spyTraceID) {
		t.Errorf("a linha não carrega o trace_id:\n  %s", line)
	}
	if !strings.Contains(line, "span_id="+spySpanID) {
		t.Errorf("a linha não carrega o span_id:\n  %s", line)
	}
	// O carimbo vai no FIM: o começo da linha é onde o olho procura tarefa e
	// tela, e empurrá-los para a direita tornaria o arquivo pior de ler.
	if !strings.HasPrefix(line, "tarefa=") {
		t.Errorf("o carimbo deslocou o começo da linha:\n  %s", line)
	}
}

// TestJournalWithoutTracerStaysUnchanged prova que o formato antigo é preservado.
//
// É o caso da máquina sem telemetria configurada — o padrão. O arquivo
// continua sendo lido por pessoa com `tail`, e um `trace_id=` vazio pendurado
// no fim seria ruído que sugere um trace inexistente.
func TestJournalWithoutTracerStaysUnchanged(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	journal := &recordingJournal{}
	// Sem WithTracer: cai no rastreador mudo, que devolve identificadores vazios.
	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithGuardrailJournal(journal))

	task, err := domain.NewTask("task-no-trace", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	if len(journal.activity) == 0 {
		t.Fatal("nenhuma linha de atividade foi escrita")
	}
	if strings.Contains(journal.activity[0], "trace_id=") {
		t.Errorf("sem rastreador a linha ganhou um trace_id vazio:\n  %s", journal.activity[0])
	}
}

// hasPrefix diz se alguma entrada começa com o prefixo.
func hasPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// contains diz se o valor exato está na lista.
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
