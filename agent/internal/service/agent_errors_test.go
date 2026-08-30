package service

import (
	"context"
	"errors"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// blockedAgent prepara uma tarefa já bloqueada, que é o ponto de partida dos
// casos de retomada.
func blockedAgent(t *testing.T, extra []ports.Completion) (*Agent, *domain.Task, *fakeStore, *fakeScreen, *fakeModel) {
	t.Helper()
	blocker := &fakeTool{
		name: "request_takeover",
		result: ports.ToolResult{
			Output:       "aguardando",
			BlockRequest: &ports.BlockRequest{Reason: domain.BlockPassword, Detail: "digite a senha"},
		},
	}
	responses := append([]ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "request_takeover", Arguments: "{}"}}},
	}, extra...)
	model := &fakeModel{responses: responses}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(model, []ports.Tool{blocker}, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if task.State != domain.StateBlocked {
		t.Fatalf("preparação falhou: esperava bloqueada, veio %s", task.State)
	}
	return agent, task, store, screen, model
}

// Se a tela estiver ocupada na retomada, o agente precisa recusar — outra
// tarefa pode ter tomado a tela enquanto a pessoa resolvia o passo.
func TestResumeRefusesWhenScreenBecameBusy(t *testing.T) {
	agent, task, _, _, _ := blockedAgent(t, nil)
	// A partir daqui a tela está ocupada por outra tarefa.
	agent.lock = &fakeLock{busy: true}
	if err := agent.Resume(context.Background(), task, ""); !errors.Is(err, domain.ErrScreenBusy) {
		t.Fatalf("esperava ErrScreenBusy, veio %v", err)
	}
}

// Conversa ausente na retomada é estado inconsistente e precisa ser reportado,
// e não silenciosamente recomeçado — recomeçar perderia o contexto do trabalho.
func TestResumeFailsWhenConversationIsMissing(t *testing.T) {
	agent, task, store, _, _ := blockedAgent(t, nil)
	delete(store.conversations, task.ID)
	if err := agent.Resume(context.Background(), task, ""); err == nil {
		t.Fatal("conversa ausente devia produzir erro")
	}
}

// Sem recado da pessoa, o agente recebe um texto padrão dizendo que o passo foi
// resolvido — sem isso ele não sabe que pode continuar.
func TestResumeAddsDefaultNoteWhenNoneGiven(t *testing.T) {
	agent, task, store, _, _ := blockedAgent(t, nil)
	if err := agent.Resume(context.Background(), task, ""); err != nil {
		t.Fatalf("Resume falhou: %v", err)
	}
	conv := store.conversations[task.ID]
	var found bool
	for _, m := range conv.Messages {
		if m.Role == domain.RoleUser && len(m.Content) > 0 &&
			m.Content != "faça algo" {
			found = true
		}
	}
	if !found {
		t.Fatal("a retomada devia acrescentar um recado ao histórico")
	}
}

// Erro do modelo durante a retomada precisa falhar a tarefa, e não deixá-la
// pendurada em estado inconsistente.
func TestResumeFailsTaskWhenModelBreaks(t *testing.T) {
	agent, task, _, _, model := blockedAgent(t, nil)
	model.err = errors.New("API fora do ar")
	if err := agent.Resume(context.Background(), task, "pronto"); err == nil {
		t.Fatal("erro do modelo devia propagar")
	}
	if task.State != domain.StateFailed {
		t.Fatalf("tarefa devia falhar, veio %s", task.State)
	}
}

// Falha ao gravar a tarefa precisa interromper: seguir adiante deixaria o
// estado em disco divergente do que está acontecendo na tela.
func TestRunFailsWhenTaskCannotBePersisted(t *testing.T) {
	store := newFakeStore()
	store.saveErr = errors.New("disco cheio")
	agent := newAgent(&fakeModel{}, nil, &fakeScreen{}, store, &fakeLock{})

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err == nil {
		t.Fatal("falha de gravação devia interromper a tarefa")
	}
}

// Tarefa que já começou não pode ser reiniciada pelo laço.
func TestRunRefusesTaskThatAlreadyStarted(t *testing.T) {
	agent := newAgent(&fakeModel{}, nil, &fakeScreen{}, newFakeStore(), &fakeLock{})
	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := task.Start(fixedClock()); err != nil {
		t.Fatalf("start falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("esperava ErrInvalidTransition, veio %v", err)
	}
}

// A resposta final do agente precisa chegar ao disco.
//
// A conversa é gravada no INÍCIO de cada iteração, então o último turno — a
// conclusão da tarefa — ficava de fora. Quem lesse o histórico depois via o
// pedido e as chamadas de ferramenta, mas nunca o que o agente concluiu.
// Descoberto rodando o binário contra a API de verdade, não em teste.
func TestFinalAssistantMessageIsPersisted(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{
		{Content: "conclui a tarefa assim", StopReason: "stop"},
	}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(model, nil, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}

	conv := store.conversations["t1"]
	if conv == nil {
		t.Fatal("conversa não foi gravada")
	}
	var found bool
	for _, m := range conv.Messages {
		if m.Role == domain.RoleAssistant && m.Content == "conclui a tarefa assim" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a resposta final devia estar no histórico gravado: %+v", conv.Messages)
	}
}

// A retomada também respeita o teto de iterações.
//
// Um agente que volta do take-over e entra em ciclo queimaria token do mesmo
// jeito que na primeira execução — o limite não pode valer só no Run.
func TestResumeStopsAtIterationLimit(t *testing.T) {
	agent, task, _, _, model := blockedAgent(t, nil)

	// A partir daqui o modelo pede ferramenta para sempre.
	loop := make([]ports.Completion, maxIterations+3)
	for i := range loop {
		loop[i] = ports.Completion{ToolCalls: []domain.ToolCall{{ID: "c", Name: "request_takeover", Arguments: "{}"}}}
	}
	model.responses = loop
	model.calls = 0
	// A ferramenta passa a devolver resultado comum, sem bloquear: é o que
	// mantém o laço girando até bater o teto.
	agent.tools["request_takeover"] = &fakeTool{name: "request_takeover", result: ports.ToolResult{Output: "de novo"}}

	if err := agent.Resume(context.Background(), task, "resolvido"); !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("esperava ErrMaxIterations, veio %v", err)
	}
}

// Falha ao gravar a conversa interrompe o laço.
//
// Seguir adiante deixaria o histórico em disco divergindo do que o modelo está
// vendo, e uma retomada depois carregaria um estado que nunca existiu.
func TestRunStopsWhenConversationCannotBeSaved(t *testing.T) {
	store := newFakeStore()
	store.conversationErr = errors.New("disco cheio")
	agent := newAgent(&fakeModel{}, nil, &fakeScreen{}, store, &fakeLock{})

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err == nil {
		t.Fatal("falha ao gravar a conversa devia interromper")
	}
}

// Conversa já existente é reaproveitada: recomeçar do zero perderia o trabalho
// feito antes de uma queda do processo.
func TestRunReusesExistingConversation(t *testing.T) {
	store := newFakeStore()
	previous := domain.NewConversation("t1", "instruções")
	previous.AddUser("trabalho anterior")
	store.conversations["t1"] = previous

	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	agent := newAgent(model, nil, &fakeScreen{}, store, &fakeLock{})

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	conv := store.conversations["t1"]
	var found bool
	for _, m := range conv.Messages {
		if m.Content == "trabalho anterior" {
			found = true
		}
	}
	if !found {
		t.Fatal("a conversa anterior devia ser reaproveitada")
	}
}
