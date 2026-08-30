package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// Erros de ciclo de vida, reconhecíveis por quem chama.
//
// São distintos de propósito: quem recebe precisa saber se procura outro id, se
// a tarefa já acabou, ou se ela nunca chegou a pedir ajuda. Um erro genérico
// obrigaria a ler a mensagem para decidir, e mensagem muda.
var (
	ErrTaskNotFound = errors.New("tarefa não encontrada")
	ErrTaskFinished = errors.New("a tarefa já está encerrada")
	ErrNotBlocked   = errors.New("a tarefa não está bloqueada")
)

// Lifecycle são as operações de tarefa que NÃO chamam o modelo: abandonar e
// reconciliar.
//
// Vive no serviço, e não no adaptador de entrada, porque a linha de comando e a
// porta HTTP precisam exatamente das mesmas regras. Duplicá-las é como as duas
// pontas divergem — e a que diverge em silêncio é sempre a que ninguém roda.
type Lifecycle struct {
	store  ports.TaskStore
	screen ports.ScreenDriver
	clock  Clock
}

// NewLifecycle monta as operações de ciclo de vida.
func NewLifecycle(store ports.TaskStore, screen ports.ScreenDriver, clock Clock) *Lifecycle {
	return &Lifecycle{store: store, screen: screen, clock: clock}
}

// Abandon desiste de uma tarefa ativa e LIBERA a tela.
//
// Liberar é o ponto, e não apenas marcar: uma tarefa abandonada que continua
// contando como ativa mantém a tela reservada, e a próxima tentativa é recusada
// sem que ninguém entenda por quê.
func (l *Lifecycle) Abandon(ctx context.Context, taskID string) (*domain.Task, error) {
	task, err := l.store.LoadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if !task.Active() {
		return nil, fmt.Errorf("%w: %s está %s", ErrTaskFinished, taskID, task.State)
	}
	if err := task.Fail("abandonada por decisão humana", l.clock()); err != nil {
		return nil, err
	}
	if err := l.store.SaveTask(ctx, task); err != nil {
		return nil, err
	}
	_ = l.screen.ClearTakeover(ctx, task.Screen)
	_ = l.screen.ShowStatus(ctx, task.Screen, task.StatusLine())
	return task, nil
}

// Reconcile conserta o estado deixado por um processo que morreu, e devolve o
// que consertou.
//
// O oráculo é a TRAVA: ela morre com o processo, o estado em disco não. Logo,
// tarefa marcada como ativa cuja tela está destravada é cadáver — ninguém a está
// rodando, por mais que o disco diga o contrário.
//
// Roda ANTES de a porta aceitar conexão. Com o servidor já no ar, isto mataria
// uma tarefa recém-criada que ainda não tomou a trava.
func (l *Lifecycle) Reconcile(ctx context.Context, lock ports.ScreenLock) ([]*domain.Task, error) {
	active, err := l.store.ListActiveTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("listando tarefas ativas: %w", err)
	}

	var fixed []*domain.Task
	for _, task := range active {
		// BLOQUEADA NÃO É CADÁVER. É o estado que a documentação exige quando
		// aparece senha, 2FA ou CAPTCHA, e ele é durável de propósito: alguém
		// precisa agir. Convertê-la em falha jogaria fora o trabalho e faria o
		// take-over deixar de existir na prática.
		//
		// O que morreu foi o aviso na tela, que era um processo. Redesenhá-lo é
		// necessário, senão a tela parece ociosa enquanto está reservada.
		if task.State == domain.StateBlocked {
			_ = l.screen.RequestTakeover(ctx, task.Screen, task.BlockReason, task.BlockDetail)
			_ = l.screen.ShowStatus(ctx, task.Screen, task.StatusLine())
			continue
		}

		release, err := lock.Acquire(ctx, task.Screen, "reconcile")
		if err != nil {
			// A trava cedeu a outro: há processo vivo de verdade nesta tela.
			continue
		}
		_ = release()

		if err := task.Fail("processo interrompido; estado reconciliado no boot", l.clock()); err != nil {
			return nil, err
		}
		if err := l.store.SaveTask(ctx, task); err != nil {
			return nil, err
		}
		_ = l.screen.ClearTakeover(ctx, task.Screen)
		_ = l.screen.ShowStatus(ctx, task.Screen, task.StatusLine())
		fixed = append(fixed, task)
	}
	return fixed, nil
}
