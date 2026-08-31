// Package api é o adaptador de ENTRADA por HTTP: recebe tarefas de outros
// sistemas e devolve o estado delas.
//
// É o primeiro adaptador driving do projeto. Até aqui a única entrada era a
// linha de comando, que fala direto com o serviço.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// defaultTaskTimeout é o teto de tempo de UMA tarefa.
//
// O teto de iterações limita quantas vezes o modelo é chamado, não quanto tempo
// isso leva. Com a delegação podendo gastar 15 minutos por chamada, 60 iterações
// seriam quinze horas segurando a tela. São recursos diferentes: iteração
// controla custo de token, isto aqui controla ocupação de tela.
const defaultTaskTimeout = 2 * time.Hour

// AgentFactory monta o agente de UMA tarefa e devolve o texto já expandido.
//
// É função, e não um agente pronto, porque o CONJUNTO DE FERRAMENTAS depende do
// TEXTO da tarefa: conector entra por "@", habilidade por "/". Um agente montado
// uma vez no boot ignoraria os dois em silêncio — a tarefa rodaria, responderia,
// e ninguém saberia que o conector pedido nunca foi anexado.
//
// A implementação vive no ponto de composição. Um adaptador de entrada que
// importasse conectores e habilidades estaria chamando o lado de saída, que a
// direção das setas proíbe.
type AgentFactory func(prompt string) (runner ports.TaskRunner, expandedPrompt string, err error)

// maxConcurrentTasks é o teto GLOBAL de tarefas rodando ao mesmo tempo.
//
// A trava de tela garante uma por tela, e são NOVE telas — o que nunca foi um
// teto de verdade. Nove tarefas simultâneas nesta máquina significam nove
// navegadores e, no pior caso, nove delegações de US$ 5,00 cada.
//
// QUATRO é medido nesta máquina (2026-08-31), não escolhido por gosto:
//
//	memória total          3.919 MB, com ~2.600 livres em repouso
//	Chrome por tela        ~370 MB (medido com duas telas de pé)
//	agentd                 282 MB
//	CPU                    2 vCPU
//
// Quatro tarefas com navegador dão ~1,5 GB de Chrome, que cabe nos 2,6 GB com
// folga para o trabalho. Nove dariam 3,3 GB e estourariam a máquina — e o modo
// de falha do estouro é o pior possível: o OOM killer escolhe a vítima, e ela
// costuma ser o processo maior, que é o próprio agentd.
//
// O limite é de tarefas EM EXECUÇÃO. Tarefa bloqueada esperando uma pessoa não
// conta: ela não gasta CPU nem token, e contá-la faria o take-over de uma tela
// impedir trabalho em outra.
const maxConcurrentTasks = 4

// ErrTooManyTasks marca recusa por teto global, e é distinta de tela ocupada.
//
// A diferença importa para quem chama: tela ocupada se resolve retomando ou
// abandonando AQUELA tarefa (409, conflito); teto global se resolve esperando
// (429, tente de novo). Misturar as duas faria o cliente tentar abandonar uma
// tarefa que não é o problema.
var ErrTooManyTasks = errors.New("tarefas demais rodando ao mesmo tempo")

// BusyError diz QUAL tarefa segura a tela.
//
// Sem isso, a recusa obriga quem chamou a adivinhar o que retomar ou abandonar —
// exatamente a queixa que fez o comando de abandono existir.
type BusyError struct{ Task *domain.Task }

// Error descreve a ocupação com o id e o estado da tarefa que segura a tela.
func (e *BusyError) Error() string {
	return fmt.Sprintf("a tela %d já tem a tarefa %s (%s)", e.Task.Screen, e.Task.ID, e.Task.State)
}

// Unwrap expõe o erro de domínio, para quem checa por tipo continuar checando.
func (e *BusyError) Unwrap() error { return domain.ErrScreenBusy }

// ErrShuttingDown recusa trabalho novo durante o encerramento.
var ErrShuttingDown = fmt.Errorf("o servidor está encerrando")

// run guarda uma tarefa em voo neste processo.
type run struct {
	task   *domain.Task
	cancel context.CancelFunc
}

// Supervisor executa tarefas em SEGUNDO PLANO e garante uma por tela.
//
// Existe porque o laço do agente é bloqueante e pode levar minutos: a requisição
// não pode esperar por ele. A parte delicada é o ciclo de vida do contexto —
// ver o comentário de Start.
type Supervisor struct {
	// base é o ciclo de vida do PROCESSO, não o de uma requisição. É a peça que
	// impede a tarefa de morrer quando o handler retorna.
	base     context.Context
	newAgent AgentFactory
	store    ports.TaskStore
	screen   ports.ScreenDriver
	lock     ports.ScreenLock
	clock    func() time.Time
	timeout  time.Duration
	log      *slog.Logger
	// maxRunning é o teto global, ajustável para o teste não depender do
	// número de produção.
	maxRunning int

	mu sync.Mutex
	// byScreen é a exclusividade que importa: uma tarefa por TELA, não por id.
	byScreen map[int]*run
	byID     map[string]*run
	wg       sync.WaitGroup
	closed   bool
}

// NewSupervisor monta o supervisor.
//
// `base` precisa ser o contexto do processo — tipicamente o que morre com
// SIGTERM. Passar `context.Background()` faria as tarefas sobreviverem ao
// encerramento e morrerem sem gravar o estado final.
func NewSupervisor(base context.Context, factory AgentFactory, store ports.TaskStore,
	screen ports.ScreenDriver, lock ports.ScreenLock, clock func() time.Time,
	timeout time.Duration, log *slog.Logger) *Supervisor {
	if timeout <= 0 {
		timeout = defaultTaskTimeout
	}
	return &Supervisor{
		base: base, newAgent: factory, store: store, screen: screen, lock: lock,
		clock: clock, timeout: timeout, log: log,
		maxRunning: envConcurrency(),
		byScreen:   map[int]*run{}, byID: map[string]*run{},
	}
}

// Start grava a tarefa, recusa se a tela estiver ocupada, e a executa em segundo
// plano. Devolve assim que a tarefa existe no disco.
//
// ⚠️ O contexto da goroutine NÃO deriva do `ctx` recebido. Este é o defeito
// número um deste tipo de adaptador, e é SILENCIOSO: o contexto de uma
// requisição morre quando o handler retorna, então a tarefa morreria na primeira
// chamada ao modelo — com o cliente já tendo recebido "criada com sucesso" e a
// tarefa marcada como falha por "context canceled".
func (s *Supervisor) Start(ctx context.Context, screenNumber int, prompt string) (*domain.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrShuttingDown
	}
	// A tela é validada ANTES da sonda de ocupação. A sonda toma a trava, e tomar
	// a trava CRIA `screen-<n>.lock` — um pedido com tela 99999999 é recusado
	// corretamente logo depois, mas deixa o arquivo no disco para sempre.
	if err := domain.ValidateScreen(screenNumber); err != nil {
		return nil, err
	}
	if err := s.screenIsFree(ctx, screenNumber); err != nil {
		return nil, err
	}

	runner, expanded, err := s.newAgent(prompt)
	if err != nil {
		return nil, err
	}
	task, err := domain.NewTask(newTaskID(s.clock()), screenNumber, expanded, s.clock())
	if err != nil {
		return nil, err
	}
	if err := s.store.SaveTask(ctx, task); err != nil {
		return nil, err
	}

	// A cópia é feita ANTES de disparar a goroutine, e a ordem é o ponto: depois
	// do spawn a corrida já começou, porque a goroutine muta o original no
	// instante seguinte enquanto quem chamou o serializa para a resposta.
	//
	// Duas mãos no mesmo objeto, sem sincronização. O detector de corrida pegou
	// exatamente isto; em produção o sintoma seria uma resposta com o estado
	// meio escrito, não um erro.
	snapshot := *task
	s.spawn(task, runner)
	return &snapshot, nil
}

// screenIsFree confere as três fontes de ocupação, todas sob o mesmo mutex.
//
// São três porque cada uma enxerga o que as outras não veem: o registro em
// memória pega o que ESTE processo roda; o disco pega tarefa bloqueada de um
// boot anterior e tarefa criada pelo CLI; a trava pega o CLI rodando AGORA em
// outro processo, cuja prova de vida não está no disco.
func (s *Supervisor) screenIsFree(ctx context.Context, screenNumber int) error {
	// O teto GLOBAL vem antes do teste de tela: recusar por "tela ocupada" uma
	// tarefa que a máquina não comportaria mandaria quem chamou tentar outra
	// tela, e a próxima falharia igual — com a mensagem errada nas duas vezes.
	if running := len(s.byScreen); running >= s.maxRunning {
		return fmt.Errorf("%w: %d rodando, teto %d", ErrTooManyTasks, running, s.maxRunning)
	}
	if r, ok := s.byScreen[screenNumber]; ok {
		return &BusyError{Task: r.task}
	}
	active, err := s.store.ActiveTaskOnScreen(ctx, screenNumber)
	if err != nil {
		return err
	}
	if active != nil {
		return &BusyError{Task: active}
	}
	// A trava é tomada e SOLTA na hora, só como sonda. Não dá para segurá-la e
	// entregar ao laço: flock é por descritor aberto, e uma segunda abertura no
	// mesmo processo também colide — o laço travaria contra a própria sonda.
	release, err := s.lock.Acquire(ctx, screenNumber, "sonda")
	if err != nil {
		return fmt.Errorf("%w (tela %d)", domain.ErrScreenBusy, screenNumber)
	}
	_ = release()
	return nil
}

// spawn põe a tarefa para rodar em segundo plano. Chamado com o mutex tomado.
func (s *Supervisor) spawn(task *domain.Task, runner ports.TaskRunner) {
	runCtx, cancel := context.WithTimeout(s.base, s.timeout)
	entry := &run{task: task, cancel: cancel}
	s.byScreen[task.Screen] = entry
	s.byID[task.ID] = entry

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		// Sair do registro é DEFER: um pânico no laço deixaria a tela marcada
		// como ocupada para sempre, e só um restart resolveria.
		defer s.forget(task.ID, task.Screen)
		// Pânico em goroutine de segundo plano derruba o PROCESSO INTEIRO,
		// levando junto as tarefas das outras telas e deixando todas em
		// "running" no disco. Recuperar aqui troca "o servidor caiu" por "uma
		// tarefa falhou".
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("pânico na tarefa", "tarefa", task.ID, "pânico", p,
					"pilha", string(debug.Stack()))
				s.markFailed(task, fmt.Sprintf("pânico: %v", p))
			}
		}()

		if err := runner.Run(runCtx, task); err != nil {
			// O erro NÃO pode sumir. O laço marca falha na maioria dos
			// caminhos, mas não em todos — falha ao iniciar, ao gravar a
			// conversa ou ao tomar a trava voltam sem tocar no estado. Sem isto,
			// a tarefa ficaria "pendente" ou "rodando" no disco com o processo
			// já livre, e a reconciliação do próximo boot teria trabalho à toa.
			s.log.Error("tarefa falhou", "tarefa", task.ID, "erro", err)
			if task.Active() {
				s.markFailed(task, err.Error())
			}
		}
	}()
}

// markFailed encerra a tarefa em erro e grava.
//
// Usa o contexto do PROCESSO, e não o da tarefa: o contexto dela pode já estar
// cancelado — é justamente por isso que ela falhou —, e gravar com contexto
// morto perderia o registro do motivo.
func (s *Supervisor) markFailed(task *domain.Task, reason string) {
	if err := task.Fail(reason, s.clock()); err != nil {
		return
	}
	if err := s.store.SaveTask(s.base, task); err != nil {
		s.log.Error("não consegui gravar a falha", "tarefa", task.ID, "erro", err)
	}
	_ = s.screen.ShowStatus(s.base, task.Screen, task.StatusLine())
}

// forget tira a tarefa do registro, liberando a tela para a próxima.
func (s *Supervisor) forget(taskID string, screenNumber int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, taskID)
	if r, ok := s.byScreen[screenNumber]; ok && r.task.ID == taskID {
		delete(s.byScreen, screenNumber)
	}
}

// Resume retoma uma tarefa bloqueada, também em segundo plano.
func (s *Supervisor) Resume(ctx context.Context, taskID, note string) (*domain.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrShuttingDown
	}
	task, err := s.store.LoadTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("%w: %s", service.ErrTaskNotFound, taskID)
	}
	if task.State != domain.StateBlocked {
		return nil, fmt.Errorf("%w: %s está %s", service.ErrNotBlocked, taskID, task.State)
	}
	if r, ok := s.byScreen[task.Screen]; ok {
		return nil, &BusyError{Task: r.task}
	}

	runner, _, err := s.newAgent(task.Prompt)
	if err != nil {
		return nil, err
	}
	// Cópia antes do disparo, pelo mesmo motivo do Start.
	snapshot := *task
	s.spawnResume(task, runner, note)
	return &snapshot, nil
}

// spawnResume põe a retomada para rodar. Chamado com o mutex tomado.
func (s *Supervisor) spawnResume(task *domain.Task, runner ports.TaskRunner, note string) {
	runCtx, cancel := context.WithTimeout(s.base, s.timeout)
	entry := &run{task: task, cancel: cancel}
	s.byScreen[task.Screen] = entry
	s.byID[task.ID] = entry

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer s.forget(task.ID, task.Screen)
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("pânico na retomada", "tarefa", task.ID, "pânico", p,
					"pilha", string(debug.Stack()))
				s.markFailed(task, fmt.Sprintf("pânico: %v", p))
			}
		}()

		if err := runner.Resume(runCtx, task, note); err != nil {
			s.log.Error("retomada falhou", "tarefa", task.ID, "erro", err)
			if task.Active() {
				s.markFailed(task, err.Error())
			}
		}
	}()
}

// Cancel derruba uma tarefa que ESTE processo está rodando.
//
// Devolve false quando ela não é nossa — o abandono ainda vale pelo disco, mas
// não interrompe nada, e quem chama precisa saber a diferença.
func (s *Supervisor) Cancel(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[taskID]
	if !ok {
		return false
	}
	r.cancel()
	return true
}

// Running conta as tarefas em voo.
//
// Existe para o teste provar que a goroutine terminou. Contar goroutines do
// runtime seria intermitente, porque o escalonador não promete quando elas
// somem.
func (s *Supervisor) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byID)
}

// Shutdown recusa trabalho novo, cancela o que está em voo e espera terminar.
//
// Esperar é o ponto: sem isso o processo sai com as tarefas no meio, elas não
// gravam o estado final, e todas ficam "rodando" no disco — dando trabalho à
// reconciliação do próximo boot por nada.
func (s *Supervisor) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for _, r := range s.byID {
		r.cancel()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// newTaskID gera um identificador ordenável por tempo.
func newTaskID(now time.Time) string {
	return fmt.Sprintf("task-%d", now.UnixNano())
}

// envConcurrency lê o teto global do ambiente, ou devolve o padrão.
//
// Ajustável porque a máquina pode crescer: num droplet maior o teto certo é
// outro, e trocá-lo não deveria exigir recompilar. Valor inválido cai no
// padrão — teto desligado por engano é o defeito que este código evita.
func envConcurrency() int {
	raw := strings.TrimSpace(os.Getenv("AGENTD_MAX_CONCURRENT_TASKS"))
	if raw == "" {
		return maxConcurrentTasks
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return maxConcurrentTasks
	}
	return value
}
