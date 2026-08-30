package service

import (
	"context"
	"errors"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// taskInState monta uma tarefa já no estado pedido, sem passar pelo laço.
func taskInState(t *testing.T, id string, screen int, state domain.TaskState) *domain.Task {
	t.Helper()
	task := mustTask(t, id, screen)
	switch state {
	case domain.StateRunning:
		if err := task.Start(fixedClock()); err != nil {
			t.Fatalf("preparação falhou: %v", err)
		}
	case domain.StateBlocked:
		if err := task.Start(fixedClock()); err != nil {
			t.Fatalf("preparação falhou: %v", err)
		}
		if err := task.Block(domain.BlockCaptcha, "resolva", fixedClock()); err != nil {
			t.Fatalf("preparação falhou: %v", err)
		}
	case domain.StatePending:
		// Já nasce pendente.
	default:
		t.Fatalf("estado não suportado na preparação: %s", state)
	}
	return task
}

// newLifecycle monta as operações com duplos.
func newLifecycle(store *fakeStore, screen *fakeScreen) *Lifecycle {
	return NewLifecycle(store, screen, fixedClock)
}

// Tarefa RODANDO cuja tela está destravada é cadáver.
//
// A trava morre com o processo, o estado em disco não. Essa assimetria é o único
// oráculo confiável para distinguir "está rodando" de "morreu rodando".
func TestReconcileFailsOrphanRunning(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	task := taskInState(t, "t1", 3, domain.StateRunning)
	store.tasks[task.ID] = task

	fixed, err := newLifecycle(store, screen).Reconcile(context.Background(), &fakeLock{})
	if err != nil {
		t.Fatalf("Reconcile falhou: %v", err)
	}
	if len(fixed) != 1 {
		t.Fatalf("esperava 1 tarefa reconciliada, veio %d", len(fixed))
	}
	if task.State != domain.StateFailed {
		t.Fatalf("devia falhar, veio %s", task.State)
	}
	if task.Active() {
		t.Fatal("a tela continua ocupada — a reconciliação não liberou nada")
	}
}

// Tarefa reconciliada AVISA — é o caso em que o aviso mais importa.
//
// Quem disparou a tarefa não tem como saber que o processo caiu: a tela volta a
// ficar livre e nada denuncia o que houve. Sem este teste a correção não vale
// nada — remova a chamada a `notify` e ele tem de reprovar.
//
// Medido em 30/08/2026: o teste da porta HTTP passava com a fila vazia, porque
// `done` é filtrado de propósito e nada mais chegava a enfileirar. O silêncio
// parecia o comportamento certo.
func TestReconcilePublishesFailureEvent(t *testing.T) {
	store, screen, sink := newFakeStore(), &fakeScreen{}, &fakeSink{}
	task := taskInState(t, "t1", 3, domain.StateRunning)
	store.tasks[task.ID] = task

	life := newLifecycle(store, screen).WithEventSink(sink)
	if _, err := life.Reconcile(context.Background(), &fakeLock{}); err != nil {
		t.Fatalf("Reconcile falhou: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("esperava 1 aviso enfileirado, veio %d", len(sink.events))
	}
	event := sink.events[0]
	if event.Kind != domain.EventFailed {
		t.Fatalf("o aviso devia ser de falha, veio %s", event.Kind)
	}
	if event.TaskID != task.ID || event.Screen != task.Screen {
		t.Fatalf("o aviso não identifica a tarefa: %+v", event)
	}
}

// Fila fora do ar NÃO impede a reconciliação.
//
// Avisar é efeito colateral. Se um erro de publicação abortasse a
// reconciliação, a tela ficaria travada para sempre por causa do canal de aviso
// — a proteção derrubando justamente o que ela existe para destravar.
func TestReconcileSurvivesSinkFailure(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	sink := &fakeSink{err: errors.New("destino fora do ar")}
	task := taskInState(t, "t1", 3, domain.StateRunning)
	store.tasks[task.ID] = task

	life := newLifecycle(store, screen).WithEventSink(sink)
	fixed, err := life.Reconcile(context.Background(), &fakeLock{})
	if err != nil {
		t.Fatalf("a fila fora do ar não devia derrubar a reconciliação: %v", err)
	}
	if len(fixed) != 1 || task.State != domain.StateFailed {
		t.Fatalf("a tarefa devia ter sido reconciliada mesmo assim: %s", task.State)
	}
}

// Tarefa BLOQUEADA não avisa de novo na reconciliação.
//
// Ela já avisou quando bloqueou, e o aviso continua pendente na fila. Repetir a
// cada boot encheria o destino de duplicatas do mesmo pedido — e um canal que
// repete é um canal que a pessoa desliga.
func TestReconcileDoesNotRepublishBlocked(t *testing.T) {
	store, screen, sink := newFakeStore(), &fakeScreen{}, &fakeSink{}
	task := taskInState(t, "t1", 2, domain.StateBlocked)
	store.tasks[task.ID] = task

	life := newLifecycle(store, screen).WithEventSink(sink)
	if _, err := life.Reconcile(context.Background(), &fakeLock{}); err != nil {
		t.Fatalf("Reconcile falhou: %v", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("bloqueada não devia gerar aviso novo: %+v", sink.events)
	}
}

// Tarefa PENDENTE órfã também é reconciliada.
//
// É o processo que morreu entre criar a tarefa e iniciá-la. Sem a transição de
// pendente para falha, ela ficaria ocupando a tela para sempre.
func TestReconcileFailsOrphanPending(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	task := taskInState(t, "t1", 1, domain.StatePending)
	store.tasks[task.ID] = task

	if _, err := newLifecycle(store, screen).Reconcile(context.Background(), &fakeLock{}); err != nil {
		t.Fatalf("Reconcile falhou: %v", err)
	}
	if task.State != domain.StateFailed {
		t.Fatalf("pendente órfã devia falhar, veio %s", task.State)
	}
}

// Tarefa com processo VIVO não é tocada.
//
// A trava recusada é a prova de vida: há outro processo trabalhando naquela
// tela, e matá-lo pelo disco destruiria trabalho em curso.
func TestReconcileLeavesLiveTaskAlone(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	task := taskInState(t, "t1", 2, domain.StateRunning)
	store.tasks[task.ID] = task

	fixed, err := newLifecycle(store, screen).Reconcile(context.Background(), &fakeLock{busy: true})
	if err != nil {
		t.Fatalf("Reconcile falhou: %v", err)
	}
	if len(fixed) != 0 {
		t.Fatalf("nada devia ser reconciliado, veio %d", len(fixed))
	}
	if task.State != domain.StateRunning {
		t.Fatalf("a tarefa viva foi alterada: %s", task.State)
	}
}

// Tarefa BLOQUEADA sobrevive ao boot, com o aviso redesenhado.
//
// É o estado que a documentação exige quando aparece senha, 2FA ou CAPTCHA, e
// ele é durável de propósito: alguém precisa agir. Convertê-la em falha jogaria
// fora o trabalho e faria o take-over deixar de existir na prática.
//
// O que morreu foi o aviso na tela, que era um processo — sem redesenhar, a tela
// pareceria ociosa enquanto está reservada.
func TestReconcileKeepsBlockedAndRedrawsTheNotice(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	task := taskInState(t, "t1", 4, domain.StateBlocked)
	store.tasks[task.ID] = task

	fixed, err := newLifecycle(store, screen).Reconcile(context.Background(), &fakeLock{})
	if err != nil {
		t.Fatalf("Reconcile falhou: %v", err)
	}
	if len(fixed) != 0 {
		t.Fatalf("bloqueada não é cadáver: %d reconciliadas", len(fixed))
	}
	if task.State != domain.StateBlocked {
		t.Fatalf("devia continuar bloqueada, veio %s", task.State)
	}
	if screen.takeovers != 1 {
		t.Fatalf("o aviso devia ser redesenhado, veio %d", screen.takeovers)
	}
}

// Duas tarefas na MESMA tela: a bloqueada fica, a órfã sai.
//
// É o caso que uma varredura por tela erraria — ela devolve só a primeira, e a
// segunda ficaria invisível até a primeira sair.
func TestReconcileHandlesTwoTasksOnSameScreen(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	blockedTask := taskInState(t, "t-bloqueada", 2, domain.StateBlocked)
	orphanTask := taskInState(t, "t-orfa", 2, domain.StatePending)
	store.tasks[blockedTask.ID] = blockedTask
	store.tasks[orphanTask.ID] = orphanTask

	if _, err := newLifecycle(store, screen).Reconcile(context.Background(), &fakeLock{}); err != nil {
		t.Fatalf("Reconcile falhou: %v", err)
	}
	if blockedTask.State != domain.StateBlocked {
		t.Fatalf("a bloqueada devia ficar intacta, veio %s", blockedTask.State)
	}
	if orphanTask.State != domain.StateFailed {
		t.Fatalf("a órfã devia ser encerrada, veio %s", orphanTask.State)
	}
}

// Falha ao listar interrompe: reconciliar com lista parcial deixaria tarefa
// presa sem ninguém saber que ela foi ignorada.
func TestReconcileReportsListFailure(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	store.listErr = errors.New("disco ilegível")
	if _, err := newLifecycle(store, screen).Reconcile(context.Background(), &fakeLock{}); err == nil {
		t.Fatal("falha ao listar devia interromper")
	}
}

// Abandonar libera a tela e devolve a tarefa encerrada.
func TestAbandonFreesTheScreen(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	task := taskInState(t, "t1", 1, domain.StateBlocked)
	store.tasks[task.ID] = task

	out, err := newLifecycle(store, screen).Abandon(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("Abandon falhou: %v", err)
	}
	if out.Active() {
		t.Fatal("a tela devia ter sido liberada")
	}
	if screen.cleared != 1 {
		t.Fatalf("o aviso devia ser removido da tela, veio %d", screen.cleared)
	}
}

// Abandonar tarefa inexistente e tarefa já encerrada são erros DISTINTOS.
//
// Quem recebe precisa saber se procura outro id ou se não há nada a fazer, e um
// erro genérico obrigaria a ler a mensagem para decidir.
func TestAbandonDistinguishesNotFoundFromFinished(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	life := newLifecycle(store, screen)

	if _, err := life.Abandon(context.Background(), "não-existe"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("esperava ErrTaskNotFound, veio %v", err)
	}

	finishedTask := taskInState(t, "t1", 1, domain.StateRunning)
	if err := finishedTask.Finish(fixedClock()); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	store.tasks[finishedTask.ID] = finishedTask
	if _, err := life.Abandon(context.Background(), finishedTask.ID); !errors.Is(err, ErrTaskFinished) {
		t.Fatalf("esperava ErrTaskFinished, veio %v", err)
	}
}

// Falha ao gravar interrompe o abandono: dizer "liberada" com o disco dizendo
// outra coisa é pior que falhar.
func TestAbandonReportsSaveFailure(t *testing.T) {
	store, screen := newFakeStore(), &fakeScreen{}
	task := taskInState(t, "t1", 1, domain.StateBlocked)
	store.tasks[task.ID] = task
	store.saveErr = errors.New("disco cheio")

	if _, err := newLifecycle(store, screen).Abandon(context.Background(), task.ID); err == nil {
		t.Fatal("falha de gravação devia interromper")
	}
}
