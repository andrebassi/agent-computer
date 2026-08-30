package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// Oito requisições simultâneas na MESMA tela: exatamente uma vence.
//
// É o teste do TOCTOU. Sem o mutex cobrindo checar E gravar, duas requisições
// leem "tela livre" ao mesmo tempo e as duas passam — e o resultado não é uma
// falha, são duas tarefas disputando o mesmo teclado, produzindo cliques
// intercalados que não falham, só fazem a coisa errada.
func TestConcurrentStartsOnlyOneWins(t *testing.T) {
	runner := &fakeRunner{release: make(chan struct{})}
	sup, _ := newSupervisor(t, runner, newFakeStore(), &fakeLock{})

	const attempts = 8
	var aceitos, recusados atomic.Int32
	var largada sync.WaitGroup
	var terminou sync.WaitGroup
	largada.Add(1)

	for i := 0; i < attempts; i++ {
		terminou.Add(1)
		go func() {
			defer terminou.Done()
			// Todas partem juntas: é o que torna a corrida provável.
			largada.Wait()
			if _, err := sup.Start(context.Background(), 1, "faça algo"); err != nil {
				if errors.Is(err, domain.ErrScreenBusy) {
					recusados.Add(1)
				}
				return
			}
			aceitos.Add(1)
		}()
	}
	largada.Done()
	terminou.Wait()

	if aceitos.Load() != 1 {
		t.Fatalf("exatamente uma devia entrar, entraram %d", aceitos.Load())
	}
	if recusados.Load() != attempts-1 {
		t.Fatalf("as outras %d deviam ser recusadas por tela ocupada, foram %d",
			attempts-1, recusados.Load())
	}
	close(runner.release)
}

// A recusa diz QUAL tarefa segura a tela.
//
// Sem isso, quem chamou precisa adivinhar o que retomar ou abandonar.
func TestBusyErrorNamesTheTaskHoldingTheScreen(t *testing.T) {
	runner := &fakeRunner{release: make(chan struct{})}
	sup, _ := newSupervisor(t, runner, newFakeStore(), &fakeLock{})

	primeira, err := sup.Start(context.Background(), 2, "primeira")
	if err != nil {
		t.Fatalf("Start falhou: %v", err)
	}
	_, err = sup.Start(context.Background(), 2, "segunda")

	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("esperava BusyError, veio %v", err)
	}
	if busy.Task.ID != primeira.ID {
		t.Fatalf("devia nomear a tarefa que segura a tela: %s", busy.Task.ID)
	}
	// E continua reconhecível pelo erro de domínio, para quem checa por tipo.
	if !errors.Is(err, domain.ErrScreenBusy) {
		t.Fatalf("BusyError devia embrulhar ErrScreenBusy: %v", err)
	}
	close(runner.release)
}

// Telas DIFERENTES rodam em paralelo — a exclusividade é por tela, não global.
func TestDifferentScreensRunInParallel(t *testing.T) {
	runner := &fakeRunner{release: make(chan struct{})}
	sup, _ := newSupervisor(t, runner, newFakeStore(), &fakeLock{})

	for _, tela := range []int{1, 2, 3} {
		if _, err := sup.Start(context.Background(), tela, "faça algo"); err != nil {
			t.Fatalf("tela %d devia ser aceita: %v", tela, err)
		}
	}
	if sup.Running() != 3 {
		t.Fatalf("esperava 3 tarefas em voo, veio %d", sup.Running())
	}
	close(runner.release)
}

// Tarefa BLOQUEADA no disco ocupa a tela, mesmo sem processo rodando.
//
// É o caso que o registro em memória não enxerga: ela sobrou de um boot anterior
// e continua reservando a tela até alguém agir.
func TestStartRefusesWhenDiskHasBlockedTask(t *testing.T) {
	store := newFakeStore()
	blockedTask, err := domain.NewTask("t-antiga", 1, "antiga", time.Now())
	if err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	_ = blockedTask.Start(time.Now())
	_ = blockedTask.Block(domain.BlockCaptcha, "resolva", time.Now())
	store.tasks[blockedTask.ID] = blockedTask

	sup, _ := newSupervisor(t, &fakeRunner{}, store, &fakeLock{})
	_, err = sup.Start(context.Background(), 1, "nova")
	if !errors.Is(err, domain.ErrScreenBusy) {
		t.Fatalf("tarefa bloqueada no disco devia ocupar a tela: %v", err)
	}
}

// Trava tomada por OUTRO PROCESSO recusa, mesmo com disco e memória limpos.
//
// É o caso do comando de linha rodando agora: a tarefa dele pode ainda não estar
// no disco, e a única prova de vida é a trava.
func TestStartRefusesWhenAnotherProcessHoldsTheLock(t *testing.T) {
	sup, _ := newSupervisor(t, &fakeRunner{}, newFakeStore(), &fakeLock{busy: true})
	if _, err := sup.Start(context.Background(), 1, "faça algo"); !errors.Is(err, domain.ErrScreenBusy) {
		t.Fatalf("trava de outro processo devia recusar: %v", err)
	}
}

// Cancelar uma tarefa que este processo roda interrompe de verdade.
func TestCancelStopsARunningTask(t *testing.T) {
	runner := &fakeRunner{entered: make(chan struct{}), release: make(chan struct{})}
	sup, _ := newSupervisor(t, runner, newFakeStore(), &fakeLock{})

	task, err := sup.Start(context.Background(), 1, "faça algo")
	if err != nil {
		t.Fatalf("Start falhou: %v", err)
	}
	<-runner.entered
	if !sup.Cancel(task.ID) {
		t.Fatal("a tarefa é deste processo; Cancel devia devolver true")
	}
	// Tarefa que não é nossa: o abandono ainda vale pelo disco, mas não
	// interrompe nada — e quem chama precisa saber a diferença.
	if sup.Cancel("id-de-outro-processo") {
		t.Fatal("tarefa desconhecida não devia reportar cancelamento")
	}
	close(runner.release)
}
