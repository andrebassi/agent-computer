package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// blockedTaskInStore prepara uma tarefa bloqueada no disco, como a que sobra
// depois de um take-over.
func blockedTaskInStore(t *testing.T, store *fakeStore, id string, screen int) *domain.Task {
	t.Helper()
	task, err := domain.NewTask(id, screen, "faça algo", time.Now())
	if err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	if err := task.Start(time.Now()); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	if err := task.Block(domain.BlockPassword, "digite a senha", time.Now()); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	store.tasks[id] = task
	return task
}

// Retomar devolve o controle ao agente, em segundo plano.
func TestResumeRunsInBackground(t *testing.T) {
	runner := &fakeRunner{entered: make(chan struct{}), release: make(chan struct{})}
	store := newFakeStore()
	blockedTaskInStore(t, store, "t1", 1)
	sup, _ := newSupervisor(t, runner, store, &fakeLock{})

	task, err := sup.Resume(context.Background(), "t1", "resolvi a senha")
	if err != nil {
		t.Fatalf("Resume falhou: %v", err)
	}
	if task.ID != "t1" {
		t.Fatalf("devolveu a tarefa errada: %s", task.ID)
	}
	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("a retomada nem começou")
	}
	close(runner.release)
	waitIdle(t, sup)
}

// Retomar tarefa inexistente e tarefa não bloqueada são erros DISTINTOS.
//
// Quem recebe precisa saber se procura outro id ou se a tarefa nunca pediu
// ajuda — um erro genérico obrigaria a ler a mensagem para decidir.
func TestResumeDistinguishesNotFoundFromNotBlocked(t *testing.T) {
	store := newFakeStore()
	sup, _ := newSupervisor(t, &fakeRunner{}, store, &fakeLock{})

	if _, err := sup.Resume(context.Background(), "não-existe", ""); !errors.Is(err, service.ErrTaskNotFound) {
		t.Fatalf("esperava ErrTaskNotFound, veio %v", err)
	}

	rodando, err := domain.NewTask("t2", 1, "faça algo", time.Now())
	if err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	if err := rodando.Start(time.Now()); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	store.tasks["t2"] = rodando
	if _, err := sup.Resume(context.Background(), "t2", ""); !errors.Is(err, service.ErrNotBlocked) {
		t.Fatalf("esperava ErrNotBlocked, veio %v", err)
	}
}

// Retomar recusa quando a tela foi tomada por OUTRA tarefa nesse meio-tempo.
//
// A pessoa pode ter demorado a agir, e outra tarefa pode ter começado ali.
func TestResumeRefusesWhenScreenBecameBusy(t *testing.T) {
	runner := &fakeRunner{release: make(chan struct{})}
	store := newFakeStore()
	blockedTaskInStore(t, store, "t-bloqueada", 1)
	sup, _ := newSupervisor(t, runner, store, &fakeLock{})

	// Outra tarefa toma a tela 1. A bloqueada está no disco, então a tela conta
	// como ocupada — este Start precisa ser recusado, e é o que prepara o caso.
	if _, err := sup.Start(context.Background(), 1, "outra"); err == nil {
		t.Fatal("preparação: a tela devia estar ocupada pela bloqueada")
	}

	// Agora simula a tela ocupada por uma tarefa em voo deste processo.
	if _, err := sup.Start(context.Background(), 2, "em voo"); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	blockedTaskInStore(t, store, "t-outra-tela", 2)
	if _, err := sup.Resume(context.Background(), "t-outra-tela", ""); !errors.Is(err, domain.ErrScreenBusy) {
		t.Fatalf("esperava recusa por tela ocupada, veio %v", err)
	}
	close(runner.release)
}

// Depois de encerrado, nada é retomado.
func TestResumeRefusedAfterShutdown(t *testing.T) {
	store := newFakeStore()
	blockedTaskInStore(t, store, "t1", 1)
	sup, _ := newSupervisor(t, &fakeRunner{}, store, &fakeLock{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sup.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown falhou: %v", err)
	}
	if _, err := sup.Resume(context.Background(), "t1", ""); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("esperava ErrShuttingDown, veio %v", err)
	}
}

// Erro na retomada marca a tarefa como falha.
func TestResumeErrorLandsOnTheTask(t *testing.T) {
	runner := &fakeRunner{returns: errors.New("modelo caiu")}
	store := newFakeStore()
	blockedTaskInStore(t, store, "t1", 1)
	sup, _ := newSupervisor(t, runner, store, &fakeLock{})

	if _, err := sup.Resume(context.Background(), "t1", "pronto"); err != nil {
		t.Fatalf("Resume falhou: %v", err)
	}
	waitIdle(t, sup)

	if store.tasks["t1"].State != domain.StateFailed {
		t.Fatalf("esperava falha, veio %s", store.tasks["t1"].State)
	}
}

// Pânico na retomada também é contido.
func TestResumePanicIsContained(t *testing.T) {
	runner := &fakeRunner{panicky: true}
	store := newFakeStore()
	blockedTaskInStore(t, store, "t1", 1)
	sup, _ := newSupervisor(t, runner, store, &fakeLock{})

	if _, err := sup.Resume(context.Background(), "t1", ""); err != nil {
		t.Fatalf("Resume falhou: %v", err)
	}
	waitIdle(t, sup)

	if store.tasks["t1"].State != domain.StateFailed {
		t.Fatalf("o pânico devia virar falha da tarefa, veio %s", store.tasks["t1"].State)
	}
}

// A mensagem de tela ocupada é legível e nomeia a tarefa.
func TestBusyErrorMessage(t *testing.T) {
	task, err := domain.NewTask("t-abc", 3, "faça algo", time.Now())
	if err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	msg := (&BusyError{Task: task}).Error()
	if msg == "" {
		t.Fatal("a mensagem não pode ser vazia")
	}
	for _, esperado := range []string{"t-abc", "3"} {
		if !strings.Contains(msg, esperado) {
			t.Fatalf("a mensagem devia conter %q: %q", esperado, msg)
		}
	}
}
