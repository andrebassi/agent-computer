package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// fakeSink guarda os fatos publicados e pode falhar sob comando.
type fakeSink struct {
	events []domain.TaskEvent
	err    error
}

// Publish registra o fato, ou devolve o erro configurado.
func (f *fakeSink) Publish(_ context.Context, event domain.TaskEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, event)
	return nil
}

// Bloquear publica um fato — é o aviso que pede uma pessoa.
//
// É o mais importante dos três: enquanto ninguém age, a tela fica RESERVADA e
// nenhuma outra tarefa roda ali. Um aviso perdido não atrasa uma tarefa,
// inutiliza uma tela.
func TestBlockPublishesEvent(t *testing.T) {
	sink := &fakeSink{}
	blocker := &fakeTool{
		name: "request_takeover",
		result: ports.ToolResult{
			Output:       "aguardando",
			BlockRequest: &ports.BlockRequest{Reason: domain.BlockCaptcha, Detail: "resolva o desafio"},
		},
	}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "request_takeover", Arguments: "{}"}}},
	}}
	agent := newAgentWithSink(t, model, []ports.Tool{blocker}, sink)

	task := mustTask(t, "t1", 2)
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("esperava 1 fato publicado, veio %d", len(sink.events))
	}
	event := sink.events[0]
	if event.Kind != domain.EventBlocked {
		t.Fatalf("espécie errada: %s", event.Kind)
	}
	if event.Reason != domain.BlockCaptcha || event.Screen != 2 {
		t.Fatalf("motivo ou tela errados: %+v", event)
	}
	// A mensagem tem de dizer que precisa de alguém, senão ninguém age.
	if msg := event.Message(); msg == "" || !strings.Contains(msg, "PRECISA DE VOCÊ") {
		t.Fatalf("a mensagem devia pedir ação: %q", msg)
	}
}

// Concluir publica o fato com a RESPOSTA da tarefa.
func TestFinishPublishesEventWithAnswer(t *testing.T) {
	sink := &fakeSink{}
	model := &fakeModel{responses: []ports.Completion{{Content: "contei 4 núcleos", StopReason: "stop"}}}
	agent := newAgentWithSink(t, model, nil, sink)

	if err := agent.Run(context.Background(), mustTask(t, "t1", 1)); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].Kind != domain.EventFinished {
		t.Fatalf("esperava um fato de conclusão: %+v", sink.events)
	}
	if sink.events[0].Summary != "contei 4 núcleos" {
		t.Fatalf("a resposta devia ir junto: %q", sink.events[0].Summary)
	}
}

// Falhar publica o fato com o motivo.
func TestFailurePublishesEvent(t *testing.T) {
	sink := &fakeSink{}
	model := &fakeModel{err: errors.New("API fora do ar")}
	agent := newAgentWithSink(t, model, nil, sink)

	if err := agent.Run(context.Background(), mustTask(t, "t1", 1)); err == nil {
		t.Fatal("erro do modelo devia propagar")
	}
	// Falha do modelo encerra por um caminho que não passa pelo settle, então o
	// fato precisa ser publicado ali também — senão a tarefa morre em silêncio.
	if len(sink.events) != 1 || sink.events[0].Kind != domain.EventFailed {
		t.Fatalf("esperava um fato de falha: %+v", sink.events)
	}
}

// Destino fora do ar NÃO derruba a tarefa.
//
// É o teste que codifica o contrato: avisar é efeito colateral. O trabalho foi
// feito e o disco já registrou; um webhook caído não pode transformar tarefa
// concluída em tarefa falhada.
func TestSinkFailureDoesNotFailTheTask(t *testing.T) {
	sink := &fakeSink{err: errors.New("webhook fora do ar")}
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	agent := newAgentWithSink(t, model, nil, sink)

	task := mustTask(t, "t1", 1)
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("falha ao avisar não devia derrubar a tarefa: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("a tarefa devia concluir, veio %s", task.State)
	}
}

// O aviso perdido fica REGISTRADO no histórico.
//
// Engolir a falha faria quem lê a conversa concluir que nunca houve o que
// avisar. A nota é o que permite descobrir depois que alguém deveria ter sido
// chamado e não foi.
func TestUndeliveredEventIsRecordedInHistory(t *testing.T) {
	sink := &fakeSink{err: errors.New("sem rede")}
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	store := newFakeStore()
	agent := NewAgent(model, nil, &fakeScreen{}, store, &fakeLock{}, fixedClock, "instruções",
		WithEventSink(sink))

	if err := agent.Run(context.Background(), mustTask(t, "t1", 1)); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	conv := store.conversations["t1"]
	var found bool
	for _, m := range conv.Messages {
		if m.Role == domain.RoleSystem && strings.Contains(m.Content, "aviso não entregue") {
			found = true
		}
	}
	if !found {
		t.Fatalf("o aviso perdido devia estar no histórico: %+v", conv.Messages)
	}
}

// Sem destino configurado, o agente funciona igual.
//
// O padrão descarta em vez de ser nil: sem isto haveria um caminho onde esquecer
// a checagem de nil vira pânico em produção.
func TestAgentWorksWithoutSink(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	agent := newAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{})
	if err := agent.Run(context.Background(), mustTask(t, "t1", 1)); err != nil {
		t.Fatalf("sem destino o agente devia funcionar igual: %v", err)
	}
}

// newAgentWithSink monta um agente com destino de eventos.
func newAgentWithSink(t *testing.T, model ports.LanguageModel, tools []ports.Tool, sink ports.EventSink) *Agent {
	t.Helper()
	return NewAgent(model, tools, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithEventSink(sink))
}

// mustTask cria uma tarefa ou aborta o teste.
func mustTask(t *testing.T, id string, screen int) *domain.Task {
	t.Helper()
	task, err := domain.NewTask(id, screen, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	return task
}
