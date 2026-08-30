package api

import (
	"context"
	"sync"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// fakeStore guarda tarefas e conversas em memória.
//
// Tem mutex, ao contrário do duplo equivalente do serviço: aqui as tarefas rodam
// em goroutines de verdade, e um mapa sem proteção faria o detector de corrida
// acusar — corretamente.
type fakeStore struct {
	mu            sync.Mutex
	tasks         map[string]*domain.Task
	conversations map[string]*domain.Conversation
	saveErr       error
}

// newFakeStore devolve um armazenamento vazio pronto para uso.
func newFakeStore() *fakeStore {
	return &fakeStore{
		tasks:         map[string]*domain.Task{},
		conversations: map[string]*domain.Conversation{},
	}
}

// SaveTask guarda a tarefa em memória.
func (f *fakeStore) SaveTask(_ context.Context, t *domain.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.tasks[t.ID] = t
	return nil
}

// LoadTask devolve a tarefa pedida, ou nil se não existir.
func (f *fakeStore) LoadTask(_ context.Context, id string) (*domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tasks[id], nil
}

// ActiveTaskOnScreen devolve a tarefa que ocupa a tela.
func (f *fakeStore) ActiveTaskOnScreen(_ context.Context, screen int) (*domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tasks {
		if t.Screen == screen && t.Active() {
			return t, nil
		}
	}
	return nil, nil
}

// ListActiveTasks devolve todas as tarefas que ainda ocupam alguma tela.
func (f *fakeStore) ListActiveTasks(context.Context) ([]*domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var active []*domain.Task
	for _, t := range f.tasks {
		if t.Active() {
			active = append(active, t)
		}
	}
	return active, nil
}

// SaveConversation guarda o histórico.
func (f *fakeStore) SaveConversation(_ context.Context, c *domain.Conversation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversations[c.TaskID] = c
	return nil
}

// LoadConversation devolve o histórico, ou nil se não existir.
func (f *fakeStore) LoadConversation(_ context.Context, taskID string) (*domain.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conversations[taskID], nil
}

// fakeLock simula a trava de tela entre processos.
type fakeLock struct {
	mu   sync.Mutex
	busy bool
	held map[int]bool
}

// Acquire toma a tela, ou recusa se já estiver tomada.
func (f *fakeLock) Acquire(_ context.Context, screen int, _ string) (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy {
		return nil, domain.ErrScreenBusy
	}
	if f.held == nil {
		f.held = map[int]bool{}
	}
	if f.held[screen] {
		return nil, domain.ErrScreenBusy
	}
	f.held[screen] = true
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.held, screen)
		return nil
	}, nil
}

// fakeScreen registra o que foi desenhado na tela.
type fakeScreen struct {
	mu        sync.Mutex
	takeovers int
	cleared   int
	lastLine  string
}

// ShowStatus guarda a última linha escrita.
func (f *fakeScreen) ShowStatus(_ context.Context, _ int, line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastLine = line
	return nil
}

// RequestTakeover conta os pedidos de ajuda desenhados.
func (f *fakeScreen) RequestTakeover(_ context.Context, _ int, _ domain.BlockReason, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takeovers++
	return nil
}

// ClearTakeover conta as remoções do aviso.
func (f *fakeScreen) ClearTakeover(_ context.Context, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared++
	return nil
}
