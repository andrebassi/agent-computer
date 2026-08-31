package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// blockingRunner segura toda tarefa até ser liberado, para várias ficarem
// rodando ao mesmo tempo.
//
// O `fakeRunner` do arquivo vizinho tem um canal só, que fecha na primeira
// entrada — serve para uma tarefa, e este teste precisa de várias simultâneas.
type blockingRunner struct {
	mu      sync.Mutex
	release chan struct{}
}

// Run bloqueia até o canal ser fechado.
func (b *blockingRunner) Run(_ context.Context, task *domain.Task) error {
	b.mu.Lock()
	release := b.release
	b.mu.Unlock()
	<-release
	_ = task.Start(time.Now())
	_ = task.Finish(time.Now())
	return nil
}

// Resume repete o comportamento do Run.
func (b *blockingRunner) Resume(ctx context.Context, task *domain.Task, _ string) error {
	return b.Run(ctx, task)
}

// newLimitedSupervisor monta um supervisor com teto global conhecido.
//
// O teto vem por parâmetro em vez do padrão de produção: amarrar o teste ao
// número 4 o quebraria na primeira vez que a máquina crescesse, e o que se
// quer verificar é o COMPORTAMENTO do teto, não o valor dele.
func newLimitedSupervisor(t *testing.T, runner ports.TaskRunner, limit int) *Supervisor {
	t.Helper()
	base, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sup := NewSupervisor(base, factoryFor(runner), newFakeStore(), &fakeScreen{},
		&fakeLock{}, fixedClock(), time.Minute, quietLogger())
	sup.maxRunning = limit
	return sup
}

// Passando do teto, a criação é RECUSADA — mesmo com tela livre.
//
// É a garantia central: a trava de tela nunca foi teto de máquina. São nove
// telas, e nove tarefas simultâneas significam nove navegadores e, no pior
// caso, nove delegações de US$ 5,00.
func TestGlobalCapRefusesBeyondLimitEvenWithFreeScreens(t *testing.T) {
	runner := &blockingRunner{release: make(chan struct{})}
	sup := newLimitedSupervisor(t, runner, 2)
	defer close(runner.release)

	// Duas telas DIFERENTES, ambas livres: só o teto global pode recusar.
	for screen := 1; screen <= 2; screen++ {
		if _, err := sup.Start(context.Background(), screen, fmt.Sprintf("t%d", screen)); err != nil {
			t.Fatalf("tela %d devia ser aceita: %v", screen, err)
		}
	}

	_, err := sup.Start(context.Background(), 3, "t3")
	if !errors.Is(err, ErrTooManyTasks) {
		t.Fatalf("a terceira devia bater no teto global, veio %v", err)
	}
	// A mensagem precisa dos dois números: sem eles, quem recebe não sabe se
	// esperar dois segundos ou repensar o trabalho.
	if !strings.Contains(err.Error(), "teto") {
		t.Errorf("a mensagem devia explicar o teto: %v", err)
	}
}

// Abaixo do teto, tudo é aceito — o outro sentido.
//
// Sem este caso, um teto quebrado que recusasse tudo passaria no de cima.
func TestGlobalCapAcceptsUpToTheLimit(t *testing.T) {
	runner := &blockingRunner{release: make(chan struct{})}
	sup := newLimitedSupervisor(t, runner, 3)
	defer close(runner.release)

	for screen := 1; screen <= 3; screen++ {
		if _, err := sup.Start(context.Background(), screen, fmt.Sprintf("t%d", screen)); err != nil {
			t.Fatalf("tela %d estava dentro do teto e foi recusada: %v", screen, err)
		}
	}
}

// Tarefa que TERMINA devolve a vaga.
//
// Sem isto o teto seria um contador que só sobe: a máquina aceitaria N tarefas
// na vida inteira e recusaria para sempre depois.
func TestFinishedTaskFreesTheSlot(t *testing.T) {
	release := make(chan struct{})
	runner := &blockingRunner{release: release}
	sup := newLimitedSupervisor(t, runner, 1)

	if _, err := sup.Start(context.Background(), 1, "primeira"); err != nil {
		t.Fatalf("a primeira devia entrar: %v", err)
	}
	if _, err := sup.Start(context.Background(), 2, "segunda"); !errors.Is(err, ErrTooManyTasks) {
		t.Fatalf("a segunda devia bater no teto: %v", err)
	}

	// Libera a primeira e espera a vaga voltar.
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		runner.release = make(chan struct{})
		close(runner.release)
		runner.mu.Unlock()
		if _, err = sup.Start(context.Background(), 2, "segunda"); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("a vaga devia ter sido devolvida: %v", err)
}

// O teto global é conferido ANTES da tela ocupada.
//
// A ordem importa para a MENSAGEM: recusar por "tela ocupada" uma tarefa que a
// máquina não comportaria mandaria quem chamou tentar outra tela — e a próxima
// falharia igual, com a mensagem errada nas duas vezes.
func TestGlobalCapIsCheckedBeforeScreenBusy(t *testing.T) {
	runner := &blockingRunner{release: make(chan struct{})}
	sup := newLimitedSupervisor(t, runner, 1)
	defer close(runner.release)

	if _, err := sup.Start(context.Background(), 1, "primeira"); err != nil {
		t.Fatalf("a primeira devia entrar: %v", err)
	}
	// A MESMA tela, que também está ocupada: as duas recusas se aplicam, e a
	// que precisa vencer é a do teto.
	_, err := sup.Start(context.Background(), 1, "segunda")
	if !errors.Is(err, ErrTooManyTasks) {
		t.Fatalf("o teto devia vencer o conflito de tela, veio %v", err)
	}
}

// A porta HTTP devolve 429, e não 409.
//
// A distinção é para quem chama: 409 se resolve retomando ou abandonando AQUELA
// tarefa; 429 se resolve esperando. Misturar faria o cliente tentar abandonar
// uma tarefa que não é a causa.
func TestHTTPReturnsTooManyRequestsNotConflict(t *testing.T) {
	runner := &blockingRunner{release: make(chan struct{})}
	sup := newLimitedSupervisor(t, runner, 1)
	defer close(runner.release)

	server, err := NewServer(sup, nil, newFakeStore(), "segredo", quietLogger())
	if err != nil {
		t.Fatalf("montando o servidor: %v", err)
	}

	criar := func(screen int) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"prompt":"faça algo","screen":%d}`, screen)
		req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer segredo")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}

	if rec := criar(1); rec.Code != http.StatusCreated {
		t.Fatalf("a primeira devia ser 201, veio %d: %s", rec.Code, rec.Body)
	}
	rec := criar(2)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("esperava 429, veio %d: %s", rec.Code, rec.Body)
	}
	// A dica precisa estar no corpo: sem ela, 429 sozinho não diz o que fazer.
	if !strings.Contains(rec.Body.String(), "espere") {
		t.Errorf("o corpo devia dizer o que fazer: %s", rec.Body)
	}
}

// Teto vindo do ambiente, com queda para o padrão em valor inválido.
func TestConcurrencyFromEnvironmentFallsBackWhenInvalid(t *testing.T) {
	cases := []struct {
		value    string
		expected int
	}{
		{"7", 7},
		{"", maxConcurrentTasks},
		{"muitas", maxConcurrentTasks},
		{"0", maxConcurrentTasks},
		{"-1", maxConcurrentTasks},
	}
	for _, c := range cases {
		t.Setenv("AGENTD_MAX_CONCURRENT_TASKS", c.value)
		if got := envConcurrency(); got != c.expected {
			t.Errorf("envConcurrency() com %q = %d, esperava %d", c.value, got, c.expected)
		}
	}
}
