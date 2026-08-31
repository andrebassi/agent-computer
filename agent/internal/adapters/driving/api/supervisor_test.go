package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/lock"
	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// fixedClock congela o tempo, mas avança um nanossegundo por chamada para os
// identificadores de tarefa não colidirem.
func fixedClock() func() time.Time {
	var n atomic.Int64
	base := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return base.Add(time.Duration(n.Add(1))) }
}

// quietLogger descarta os registros: o teste não precisa deles, e imprimi-los
// esconderia a saída que importa.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeRunner substitui o laço inteiro.
//
// É o que o porto de entrada torna possível: sem ele, testar a porta exigiria
// rodar o modelo de verdade, e o teste voltaria a custar token.
type fakeRunner struct {
	entered chan struct{}
	release chan struct{}
	// seenErr guarda o ctx.Err() observado DEPOIS que a requisição terminou. É
	// a asserção central: precisa ser nil.
	seenErr error
	panicky bool
	returns error
	mu      sync.Mutex
}

// Run sinaliza a entrada, espera liberação e registra o estado do contexto.
func (f *fakeRunner) Run(ctx context.Context, task *domain.Task) error {
	if f.entered != nil {
		close(f.entered)
	}
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	f.seenErr = ctx.Err()
	f.mu.Unlock()
	if f.panicky {
		panic("ferramenta explodiu")
	}
	if f.returns != nil {
		return f.returns
	}
	_ = task.Start(time.Now())
	_ = task.Finish(time.Now())
	return nil
}

// Resume repete o comportamento do Run, para os testes de retomada.
func (f *fakeRunner) Resume(ctx context.Context, task *domain.Task, _ string) error {
	return f.Run(ctx, task)
}

// observedContextError devolve o erro de contexto visto pelo laço.
func (f *fakeRunner) observedContextError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seenErr
}

// factoryFor devolve sempre o mesmo laço falso.
func factoryFor(runner ports.TaskRunner) AgentFactory {
	return func(prompt string) (ports.TaskRunner, string, error) {
		return runner, prompt, nil
	}
}

// newSupervisor monta o supervisor com duplos e um contexto de processo.
func newSupervisor(t *testing.T, runner ports.TaskRunner, store ports.TaskStore, lock ports.ScreenLock) (*Supervisor, context.CancelFunc) {
	t.Helper()
	base, cancel := context.WithCancel(context.Background())
	sup := NewSupervisor(base, factoryFor(runner), store, &fakeScreen{}, lock,
		fixedClock(), time.Minute, quietLogger())
	t.Cleanup(cancel)
	return sup, cancel
}

// A criação responde ANTES de o laço terminar.
//
// O laço pode levar minutos; a requisição não pode esperar por ele.
func TestStartRespondsBeforeRunFinishes(t *testing.T) {
	runner := &fakeRunner{entered: make(chan struct{}), release: make(chan struct{})}
	sup, _ := newSupervisor(t, runner, newFakeStore(), &fakeLock{})

	task, err := sup.Start(context.Background(), 1, "faça algo")
	if err != nil {
		t.Fatalf("Start falhou: %v", err)
	}
	if task.ID == "" {
		t.Fatal("a tarefa devia ter identificador")
	}
	// Se Start esperasse o laço, este ponto nunca seria alcançado.
	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("o laço nem começou")
	}
	close(runner.release)
}

// A tarefa SOBREVIVE ao fim da requisição.
//
// É o teste central deste adaptador. O contexto de uma requisição morre quando o
// handler retorna; se a goroutine derivasse dele, a tarefa morreria na primeira
// chamada ao modelo — com o cliente já tendo recebido "criada" e a tarefa
// marcada como falha por "context canceled". Falha silenciosa e difícil de
// atribuir.
func TestRunSurvivesRequestContextCancel(t *testing.T) {
	runner := &fakeRunner{entered: make(chan struct{}), release: make(chan struct{})}
	sup, _ := newSupervisor(t, runner, newFakeStore(), &fakeLock{})

	// Simula o ciclo de vida de uma requisição: o contexto morre logo depois.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	if _, err := sup.Start(reqCtx, 1, "faça algo"); err != nil {
		t.Fatalf("Start falhou: %v", err)
	}
	<-runner.entered
	cancelReq()
	// Deixa o cancelamento se propagar, se for para se propagar.
	time.Sleep(50 * time.Millisecond)
	close(runner.release)

	waitIdle(t, sup)
	if err := runner.observedContextError(); err != nil {
		t.Fatalf("a tarefa herdou o contexto da REQUISIÇÃO e morreu com ela: %v", err)
	}
}

// Pânico é contido e vira falha da tarefa, não queda do processo.
//
// Pânico em goroutine de segundo plano derruba o processo inteiro, levando junto
// as tarefas das outras telas e deixando todas em "rodando" no disco.
func TestPanicIsContainedAndRecorded(t *testing.T) {
	runner := &fakeRunner{panicky: true}
	store := newFakeStore()
	sup, _ := newSupervisor(t, runner, store, &fakeLock{})

	task, err := sup.Start(context.Background(), 1, "faça algo")
	if err != nil {
		t.Fatalf("Start falhou: %v", err)
	}
	waitIdle(t, sup)

	// O próprio teste terminando já prova que o processo sobreviveu.
	stored := store.tasks[task.ID]
	if stored.State != domain.StateFailed {
		t.Fatalf("a tarefa devia constar como falha, veio %s", stored.State)
	}
}

// A tela é liberada mesmo depois de um pânico.
//
// Sem o `defer` de saída do registro, a tela ficaria ocupada para sempre e só um
// restart resolveria.
func TestScreenIsFreedAfterPanic(t *testing.T) {
	runner := &fakeRunner{panicky: true}
	sup, _ := newSupervisor(t, runner, newFakeStore(), &fakeLock{})

	if _, err := sup.Start(context.Background(), 1, "primeira"); err != nil {
		t.Fatalf("Start falhou: %v", err)
	}
	waitIdle(t, sup)

	// A segunda tarefa na MESMA tela precisa ser aceita.
	if _, err := sup.Start(context.Background(), 1, "segunda"); err != nil {
		t.Fatalf("a tela devia ter sido liberada: %v", err)
	}
}

// Erro do laço que não tocou no estado ainda assim marca a tarefa como falha.
//
// O laço marca falha na maioria dos caminhos, mas não em todos. Sem isto, a
// tarefa ficaria "pendente" no disco com o processo já livre.
func TestRunErrorLandsOnTheTask(t *testing.T) {
	runner := &fakeRunner{returns: errors.New("modelo caiu")}
	store := newFakeStore()
	sup, _ := newSupervisor(t, runner, store, &fakeLock{})

	task, err := sup.Start(context.Background(), 1, "faça algo")
	if err != nil {
		t.Fatalf("Start falhou: %v", err)
	}
	waitIdle(t, sup)

	stored := store.tasks[task.ID]
	if stored.State != domain.StateFailed {
		t.Fatalf("esperava falha, veio %s", stored.State)
	}
	if stored.Failure == "" {
		t.Fatal("o motivo devia estar registrado")
	}
}

// Encerrar cancela o que está em voo e ESPERA terminar.
//
// Sem esperar, o processo sai com as tarefas no meio, elas não gravam o estado
// final, e todas ficam "rodando" no disco.
func TestShutdownCancelsAndWaits(t *testing.T) {
	runner := &fakeRunner{entered: make(chan struct{})}
	sup, _ := newSupervisor(t, runner, newFakeStore(), &fakeLock{})

	if _, err := sup.Start(context.Background(), 1, "faça algo"); err != nil {
		t.Fatalf("Start falhou: %v", err)
	}
	<-runner.entered

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sup.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown falhou: %v", err)
	}
	if sup.Running() != 0 {
		t.Fatalf("ainda há %d tarefa(s) em voo", sup.Running())
	}
	// Depois de encerrado, nada novo entra.
	if _, err := sup.Start(context.Background(), 2, "outra"); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("esperava ErrShuttingDown, veio %v", err)
	}
}

// waitIdle espera as tarefas em voo terminarem.
func waitIdle(t *testing.T, sup *Supervisor) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if sup.Running() == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("ainda há %d tarefa(s) em voo", sup.Running())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Tela fora do intervalo NÃO deixa arquivo de trava no disco.
//
// A sonda de ocupação toma a trava, e tomar a trava CRIA o arquivo. Com a
// validação depois da sonda, um pedido de tela 99999999 era corretamente
// recusado e mesmo assim deixava `screen-99999999.lock` para sempre — medido em
// 31/08/2026, com o diretório guardando também `screen--1.lock`.
//
// O teste usa a trava REAL, e não um dublê: o defeito é o arquivo em disco, e um
// dublê não teria arquivo nenhum para conferir.
func TestInvalidScreenLeavesNoLockFile(t *testing.T) {
	dir := t.TempDir()
	realLock, err := lock.NewFileLock(dir)
	if err != nil {
		t.Fatalf("montando a trava: %v", err)
	}
	sup, _ := newSupervisor(t, &fakeRunner{}, newFakeStore(), realLock)

	for _, screen := range []int{-1, 0, 10, 99999999} {
		if _, err := sup.Start(context.Background(), screen, "faça algo"); err == nil {
			t.Fatalf("tela %d devia ser recusada", screen)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("lendo o diretório: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("tela inválida não devia criar arquivo: %v", names)
	}

	// O outro sentido: tela VÁLIDA continua criando a trava. Sem isto, uma
	// validação que recusasse tudo passaria neste teste.
	if _, err := sup.Start(context.Background(), 1, "faça algo"); err != nil {
		t.Fatalf("tela válida devia ser aceita: %v", err)
	}
	if _, err := os.Stat(dir + "/screen-1.lock"); err != nil {
		t.Errorf("a tela válida devia ter criado a trava: %v", err)
	}
}
