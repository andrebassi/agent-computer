package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// fixedClock congela o tempo. Teste que usa o relógio real compara carimbos que
// mudam a cada execução e falha de forma intermitente.
func fixedClock() time.Time {
	return time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
}

// fakeModel devolve respostas roteirizadas, uma por chamada. É o que permite
// exercitar o laço sem gastar token nem depender de rede.
type fakeModel struct {
	responses []ports.Completion
	calls     int
	err       error
}

// Complete entrega a próxima resposta do roteiro. Depois da última, responde sem
// ferramenta, que é como o laço entende "terminei".
func (f *fakeModel) Complete(_ context.Context, _ []domain.Message, _ []ports.ToolSpec) (*ports.Completion, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.calls >= len(f.responses) {
		f.calls++
		return &ports.Completion{Content: "terminei", StopReason: "stop"}, nil
	}
	r := f.responses[f.calls]
	f.calls++
	return &r, nil
}

// fakeTool devolve um resultado fixo e conta quantas vezes foi chamada.
type fakeTool struct {
	name   string
	result ports.ToolResult
	calls  int
	err    error
}

// Spec descreve a ferramenta falsa.
func (f *fakeTool) Spec() ports.ToolSpec {
	return ports.ToolSpec{Name: f.name, Description: "ferramenta de teste", Schema: `{"type":"object"}`}
}

// Execute registra a chamada e devolve o resultado combinado.
func (f *fakeTool) Execute(_ context.Context, _ int, _ string) (*ports.ToolResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	r := f.result
	return &r, nil
}

// fakeScreen registra o que teria sido desenhado na tela.
type fakeScreen struct {
	statuses  []string
	takeovers int
	cleared   int
}

// ShowStatus guarda a linha de status pedida.
func (f *fakeScreen) ShowStatus(_ context.Context, _ int, line string) error {
	f.statuses = append(f.statuses, line)
	return nil
}

// RequestTakeover conta os pedidos de ajuda mostrados.
func (f *fakeScreen) RequestTakeover(_ context.Context, _ int, _ domain.BlockReason, _ string) error {
	f.takeovers++
	return nil
}

// ClearTakeover conta as devoluções de controle.
func (f *fakeScreen) ClearTakeover(_ context.Context, _ int) error {
	f.cleared++
	return nil
}

// fakeStore guarda tarefas e conversas em memória.
type fakeStore struct {
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
	if f.saveErr != nil {
		return f.saveErr
	}
	f.tasks[t.ID] = t
	return nil
}

// LoadTask devolve a tarefa pedida, ou nil se não existir.
func (f *fakeStore) LoadTask(_ context.Context, id string) (*domain.Task, error) {
	return f.tasks[id], nil
}

// ActiveTaskOnScreen devolve a tarefa que ocupa a tela.
func (f *fakeStore) ActiveTaskOnScreen(_ context.Context, screen int) (*domain.Task, error) {
	for _, t := range f.tasks {
		if t.Screen == screen && t.Active() {
			return t, nil
		}
	}
	return nil, nil
}

// SaveConversation guarda uma CÓPIA do histórico.
//
// Copiar não é preciosismo: o armazenamento real serializa para disco, então o
// que ficou gravado não muda quando o objeto em memória muda. Um duplo que
// guardasse o ponteiro refletiria mutações posteriores e passaria a aprovar
// código que esqueceu de gravar — foi exatamente o que aconteceu, e um canário
// pegou o teste aprovando com a gravação removida.
func (f *fakeStore) SaveConversation(_ context.Context, c *domain.Conversation) error {
	snapshot := &domain.Conversation{
		TaskID:   c.TaskID,
		Messages: append([]domain.Message(nil), c.Messages...),
	}
	f.conversations[c.TaskID] = snapshot
	return nil
}

// LoadConversation devolve o histórico, ou nil se não existir.
func (f *fakeStore) LoadConversation(_ context.Context, taskID string) (*domain.Conversation, error) {
	return f.conversations[taskID], nil
}

// fakeLock permite simular tela ocupada e conferir que a trava foi liberada.
type fakeLock struct {
	busy     bool
	acquired int
	released int
}

// Acquire toma a tela, ou recusa se ela estiver marcada como ocupada.
func (f *fakeLock) Acquire(_ context.Context, _ int, _ string) (func() error, error) {
	if f.busy {
		return nil, domain.ErrScreenBusy
	}
	f.acquired++
	return func() error { f.released++; return nil }, nil
}

// newAgent monta o agente com os duplos usados na maioria dos casos.
func newAgent(model ports.LanguageModel, tools []ports.Tool, screen *fakeScreen, store *fakeStore, lock *fakeLock) *Agent {
	return NewAgent(model, tools, screen, store, lock, fixedClock, "instruções")
}

// O caminho mais simples: o modelo responde sem pedir ferramenta, e a tarefa
// termina.
func TestRunFinishesWhenModelStopsCallingTools(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(model, nil, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("estado final devia ser done, veio %s", task.State)
	}
	if lock.released != lock.acquired || lock.acquired == 0 {
		t.Fatalf("trava desbalanceada: %d tomadas, %d liberadas", lock.acquired, lock.released)
	}
}

// A cláusula central da documentação: ao pedir take-over, o agente PARA. Se ele
// continuasse chamando o modelo, estaria tentando contornar a barreira, que é
// exatamente o proibido.
func TestRunStopsImmediatelyOnTakeover(t *testing.T) {
	blocker := &fakeTool{
		name: "request_takeover",
		result: ports.ToolResult{
			Output:       "aguardando a pessoa",
			BlockRequest: &ports.BlockRequest{Reason: domain.BlockCaptcha, Detail: "resolva o desafio"},
		},
	}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "request_takeover", Arguments: "{}"}}},
		// Esta segunda resposta NÃO deve ser consumida: se for, o laço não parou.
		{Content: "não deveria chegar aqui"},
	}}
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
		t.Fatalf("tarefa devia estar bloqueada, veio %s", task.State)
	}
	if model.calls != 1 {
		t.Fatalf("o laço devia parar após o bloqueio; o modelo foi chamado %d vezes", model.calls)
	}
	if screen.takeovers != 1 {
		t.Fatalf("a tela devia mostrar o pedido de ajuda uma vez, veio %d", screen.takeovers)
	}
	if lock.released == 0 {
		t.Fatal("a trava da tela precisa ser liberada mesmo com a tarefa bloqueada")
	}
}

// Tela ocupada tem de recusar a tarefa nova, e não esperar em silêncio.
func TestRunRefusesWhenScreenIsBusy(t *testing.T) {
	model := &fakeModel{}
	store, screen := newFakeStore(), &fakeScreen{}
	agent := newAgent(model, nil, screen, store, &fakeLock{busy: true})

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	err = agent.Run(context.Background(), task)
	if !errors.Is(err, domain.ErrScreenBusy) {
		t.Fatalf("esperava ErrScreenBusy, veio %v", err)
	}
	if model.calls != 0 {
		t.Fatal("o modelo não devia ser chamado com a tela ocupada")
	}
}

// Falha de ferramenta não pode derrubar a tarefa: `grep` sem resultado já sai
// com código 1, e abortar aí tornaria o agente inútil.
func TestToolFailureDoesNotKillTheTask(t *testing.T) {
	failing := &fakeTool{name: "shell", err: errors.New("comando explodiu")}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "shell", Arguments: "{}"}}},
	}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(model, []ports.Tool{failing}, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run não devia falhar por erro de ferramenta: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("a tarefa devia seguir até o fim, veio %s", task.State)
	}
	// O erro precisa ter chegado ao histórico, senão o modelo não tem como se
	// corrigir na iteração seguinte.
	conv := store.conversations["t1"]
	if conv == nil {
		t.Fatal("conversa não foi gravada")
	}
	var achou bool
	for _, m := range conv.Messages {
		if strings.Contains(m.Content, "comando explodiu") {
			achou = true
		}
	}
	if !achou {
		t.Fatal("o erro da ferramenta devia aparecer no histórico")
	}
}

// Modelo inventa nome de ferramenta. Dizer isso a ele é mais útil que abortar.
func TestUnknownToolIsReportedBackToTheModel(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "ferramenta_que_nao_existe", Arguments: "{}"}}},
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
	var achou bool
	for _, m := range conv.Messages {
		if strings.Contains(m.Content, "ferramenta desconhecida") {
			achou = true
		}
	}
	if !achou {
		t.Fatal("o histórico devia informar que a ferramenta não existe")
	}
}

// Motivo de bloqueio inválido não pode parar a tarefa em silêncio: ela ficaria
// travada sem a tela saber o que pedir à pessoa.
func TestInvalidBlockReasonDoesNotBlockSilently(t *testing.T) {
	bad := &fakeTool{
		name: "request_takeover",
		result: ports.ToolResult{
			Output:       "pedindo ajuda",
			BlockRequest: &ports.BlockRequest{Reason: domain.BlockReason("inventado"), Detail: "x"},
		},
	}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "request_takeover", Arguments: "{}"}}},
	}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(model, []ports.Tool{bad}, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if task.State == domain.StateBlocked {
		t.Fatal("motivo inválido não podia bloquear a tarefa")
	}
	if screen.takeovers != 0 {
		t.Fatal("a tela não devia mostrar pedido de ajuda com motivo inválido")
	}
}

// Erro do modelo encerra a tarefa em falha, e o motivo fica registrado.
func TestModelErrorFailsTheTask(t *testing.T) {
	model := &fakeModel{err: errors.New("API fora do ar")}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(model, nil, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err == nil {
		t.Fatal("erro do modelo devia propagar")
	}
	if task.State != domain.StateFailed {
		t.Fatalf("tarefa devia falhar, veio %s", task.State)
	}
}

// Sem teto de iterações, um agente que erra a mesma chamada em ciclo queima
// dinheiro em token até alguém perceber.
func TestRunStopsAtIterationLimit(t *testing.T) {
	loop := &fakeTool{name: "shell", result: ports.ToolResult{Output: "de novo"}}
	// Roteiro maior que o teto, sempre pedindo ferramenta.
	responses := make([]ports.Completion, maxIterations+5)
	for i := range responses {
		responses[i] = ports.Completion{ToolCalls: []domain.ToolCall{{ID: "c", Name: "shell", Arguments: "{}"}}}
	}
	model := &fakeModel{responses: responses}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(model, []ports.Tool{loop}, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("esperava ErrMaxIterations, veio %v", err)
	}
	if model.calls != maxIterations {
		t.Fatalf("o modelo devia ser chamado exatamente %d vezes, veio %d", maxIterations, model.calls)
	}
}

// Retomar depois do take-over limpa o aviso da tela e continua a tarefa.
func TestResumeClearsTakeoverAndContinues(t *testing.T) {
	blocker := &fakeTool{
		name: "request_takeover",
		result: ports.ToolResult{
			Output:       "aguardando",
			BlockRequest: &ports.BlockRequest{Reason: domain.BlockPassword, Detail: "digite a senha"},
		},
	}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "request_takeover", Arguments: "{}"}}},
	}}
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
		t.Fatalf("preparação falhou: tarefa devia estar bloqueada, veio %s", task.State)
	}

	if err := agent.Resume(context.Background(), task, "senha digitada"); err != nil {
		t.Fatalf("Resume falhou: %v", err)
	}
	if screen.cleared == 0 {
		t.Fatal("o aviso da tela devia ser removido ao retomar")
	}
	if task.State != domain.StateDone {
		t.Fatalf("após retomar, a tarefa devia concluir, veio %s", task.State)
	}
}

// Retomar tarefa que não está bloqueada é erro de quem chamou.
func TestResumeRefusesTaskThatIsNotBlocked(t *testing.T) {
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(&fakeModel{}, nil, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Resume(context.Background(), task, ""); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("esperava ErrInvalidTransition, veio %v", err)
	}
}

// A linha de status precisa chegar à tela em todo passo — é o "current status"
// que a documentação pede na visualização.
func TestStatusIsPushedToTheScreen(t *testing.T) {
	tool := &fakeTool{name: "shell", result: ports.ToolResult{Output: "ok"}}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "c1", Name: "shell", Arguments: "{}"}}},
	}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := newAgent(model, []ports.Tool{tool}, screen, store, lock)

	task, err := domain.NewTask("t1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if len(screen.statuses) < 2 {
		t.Fatalf("a tela devia receber status em vários passos, veio %d", len(screen.statuses))
	}
	var mostrouFerramenta bool
	for _, s := range screen.statuses {
		if strings.Contains(s, "shell") {
			mostrouFerramenta = true
		}
	}
	if !mostrouFerramenta {
		t.Fatal("o status devia dizer qual ferramenta está em uso")
	}
}
