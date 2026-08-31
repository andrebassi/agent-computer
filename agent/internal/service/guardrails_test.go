package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// recordingJournal captura o que o laço grava, para o teste asseverar sobre o
// conteúdo em vez de sobre o disco.
type recordingJournal struct {
	mu       sync.Mutex
	lessons  []string
	errs     []string
	activity []string
	progress []string
	// failLesson força erro na gravação da lição, para exercitar o caminho em
	// que aprender falha e o bloqueio precisa sobreviver.
	failLesson bool
}

// LearnLesson guarda a lição, ou falha quando o dublê foi configurado para isso.
func (r *recordingJournal) LearnLesson(_ context.Context, kind GuardrailKind, lesson string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failLesson {
		return errors.New("disco cheio")
	}
	r.lessons = append(r.lessons, string(kind)+": "+lesson)
	return nil
}

// RecordError guarda a linha de erro.
func (r *recordingJournal) RecordError(_ context.Context, line string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, line)
	return nil
}

// RecordActivity guarda a linha de atividade.
func (r *recordingJournal) RecordActivity(_ context.Context, line string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activity = append(r.activity, line)
	return nil
}

// RecordProgress guarda a linha de progresso.
func (r *recordingJournal) RecordProgress(_ context.Context, line string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, line)
	return nil
}

// Lessons devolve as lições gravadas, no formato em que elas entram no prompt.
func (r *recordingJournal) Lessons() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lessons, "\n"), nil
}

// joined devolve tudo o que foi gravado, para busca por substring.
func (r *recordingJournal) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(append(append(append([]string{}, r.lessons...), r.errs...), r.activity...), "\n")
}

// --- detector 1: teto acumulado de turnos ---------------------------------

// Abaixo do teto, o detector NÃO dispara.
//
// É a metade que impede o guardrail de virar obstáculo: uma verificação que
// reprova qualquer entrada é "segura" e inútil, e a primeira coisa que alguém
// faz é desligá-la.
func TestTurnCapStaysQuietBelowLimit(t *testing.T) {
	task := &domain.Task{TurnsUsed: maxTurnsPerTask - 1}
	if hit := checkTurnCap(task); hit != nil {
		t.Fatalf("não devia disparar com %d turnos: %v", task.TurnsUsed, hit.Detail)
	}
}

// No teto, dispara e o detalhe traz os dois números.
//
// Sem o número usado E o teto, a pessoa que lê a tela não sabe se faltou pouco
// ou se a tarefa estava girando havia horas.
func TestTurnCapFiresAtLimitWithBothNumbers(t *testing.T) {
	task := &domain.Task{TurnsUsed: maxTurnsPerTask}
	hit := checkTurnCap(task)
	if hit == nil {
		t.Fatal("devia disparar no teto")
	}
	if hit.Kind != GuardrailTurnCap {
		t.Fatalf("tipo errado: %s", hit.Kind)
	}
	for _, expected := range []string{"180", "teto"} {
		if !strings.Contains(hit.Detail, expected) {
			t.Errorf("o detalhe devia citar %q: %s", expected, hit.Detail)
		}
	}
}

// --- detector 2: ferramenta em laço ---------------------------------------

// Duas falhas idênticas ainda não são laço; a terceira é.
//
// O limiar em três é deliberado: a segunda tentativa é comportamento legítimo —
// o modelo corrige o caminho e acerta. Este teste fixa a fronteira, para que
// mexer no número quebre algo visível.
func TestToolLoopFiresOnlyOnThirdIdenticalFailure(t *testing.T) {
	guard := newGuardrailState(time.Now(), 0)
	for attempt := 1; attempt <= 2; attempt++ {
		repeats := guard.observeToolFailure("shell", `{"command":"ls /naoexiste"}`)
		if hit := checkToolLoop("shell", "args", "erro", repeats); hit != nil {
			t.Fatalf("disparou cedo demais, na tentativa %d", attempt)
		}
	}
	repeats := guard.observeToolFailure("shell", `{"command":"ls /naoexiste"}`)
	hit := checkToolLoop("shell", "args", "No such file or directory", repeats)
	if hit == nil {
		t.Fatal("a terceira falha idêntica devia disparar")
	}
	if !strings.Contains(hit.Detail, "No such file or directory") {
		t.Errorf("o erro literal devia estar no detalhe: %s", hit.Detail)
	}
	if !strings.Contains(hit.Lesson, "shell") {
		t.Errorf("a lição devia nomear a ferramenta: %s", hit.Lesson)
	}
}

// A mensagem do bloqueio é lida por gente, e o número concorda com o
// substantivo.
//
// O caso de 1 não é hipotético: é exatamente o que a suíte de máquina produz ao
// baixar o teto por variável de ambiente, e o texto acaba colado em
// acompanhamento e relatório.
func TestToolLoopAgreesInNumber(t *testing.T) {
	// A contagem 1 chega aqui quando o teto é baixado por variável de ambiente,
	// então o singular é caminho real e não hipótese.
	if got := repeatPhrase(1); got != "1 vez seguida" {
		t.Errorf("singular errado: %q", got)
	}
	if got := repeatPhrase(3); got != "3 vezes seguidas" {
		t.Errorf("plural errado: %q", got)
	}
	hit := checkToolLoop("shell", "args", "erro", identicalFailures)
	if hit == nil || !strings.Contains(hit.Detail, repeatPhrase(identicalFailures)) {
		t.Errorf("o detalhe devia trazer o número concordado: %+v", hit)
	}
}

// Sucesso no meio ZERA a contagem.
//
// É o que separa "insiste no mesmo erro" de "erra enquanto explora". Sem isto,
// uma tarefa longa e legítima acumularia falhas esparsas até bloquear sozinha.
func TestToolLoopResetsAfterSuccess(t *testing.T) {
	guard := newGuardrailState(time.Now(), 0)
	guard.observeToolFailure("shell", "mesmo")
	guard.observeToolFailure("shell", "mesmo")
	guard.observeToolSuccess()
	if repeats := guard.observeToolFailure("shell", "mesmo"); repeats != 1 {
		t.Fatalf("o sucesso devia zerar a contagem, veio %d", repeats)
	}
}

// Falha DIFERENTE também zera — argumentos distintos não são o mesmo laço.
func TestToolLoopResetsWhenArgumentsChange(t *testing.T) {
	guard := newGuardrailState(time.Now(), 0)
	guard.observeToolFailure("shell", `{"command":"a"}`)
	guard.observeToolFailure("shell", `{"command":"a"}`)
	if repeats := guard.observeToolFailure("shell", `{"command":"b"}`); repeats != 1 {
		t.Fatalf("argumento diferente devia zerar, veio %d", repeats)
	}
}

// A MESMA ferramenta com argumentos diferentes tem chaves diferentes.
func TestFailureKeySeparatesArguments(t *testing.T) {
	if failureKey("shell", "a") == failureKey("shell", "b") {
		t.Fatal("argumentos diferentes deviam produzir chaves diferentes")
	}
	if failureKey("shell", "a") != failureKey("shell", "a") {
		t.Fatal("a mesma entrada devia produzir a mesma chave")
	}
}

// --- detector 3: tempo de parede ------------------------------------------

// Sem orçamento, o detector fica calado.
//
// É o caso do CLI, que não tem teto de tempo. Inventar um aqui mudaria o
// comportamento de um caminho que ninguém pediu para mudar.
func TestWallClockIsDisabledWithoutBudget(t *testing.T) {
	guard := newGuardrailState(time.Now(), 0)
	if hit := guard.checkWallClock(time.Now().Add(100 * time.Hour)); hit != nil {
		t.Fatalf("sem orçamento não devia disparar: %v", hit.Detail)
	}
}

// Antes da fração, calado; depois dela, dispara.
func TestWallClockFiresOnlyAfterFraction(t *testing.T) {
	start := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	guard := newGuardrailState(start, 100*time.Minute)

	if hit := guard.checkWallClock(start.Add(79 * time.Minute)); hit != nil {
		t.Fatalf("79%% do orçamento não devia disparar: %v", hit.Detail)
	}
	hit := guard.checkWallClock(start.Add(81 * time.Minute))
	if hit == nil {
		t.Fatal("81%% do orçamento devia disparar")
	}
	if hit.Kind != GuardrailWallClock {
		t.Fatalf("tipo errado: %s", hit.Kind)
	}
}

// --- resposta truncada -----------------------------------------------------

// Os rótulos de truncamento dos fornecedores são reconhecidos; "stop" não é.
//
// O caso que motiva: uma resposta cortada por limite de saída chega SEM chamada
// de ferramenta, e o laço tratava isso como conclusão. A tarefa terminava
// `done`, com a resposta pela metade e ninguém sabendo.
func TestTruncatedStopRecognizesProviderLabels(t *testing.T) {
	cases := map[string]bool{
		"length":            true,
		"max_tokens":        true,
		"MAX_OUTPUT_TOKENS": true,
		"  length  ":        true,
		"stop":              false,
		"tool_calls":        false,
		"":                  false,
	}
	for reason, expected := range cases {
		if got := truncatedStop(reason); got != expected {
			t.Errorf("truncatedStop(%q) = %v, esperava %v", reason, got, expected)
		}
	}
}

// --- integração no laço ----------------------------------------------------

// O laço BLOQUEIA quando a mesma ferramenta falha três vezes, e a tarefa fica
// recuperável.
//
// Este é o teste que prova o caminho inteiro: `ToolResult.Failed` lido, contagem
// acumulada, bloqueio pela mesma máquina do take-over, e lição gravada.
func TestLoopBlocksOnRepeatedToolFailure(t *testing.T) {
	journal := &recordingJournal{}
	failing := &fakeTool{
		name:   "shell",
		result: ports.ToolResult{Output: "ls: No such file or directory", Failed: true},
	}
	// O modelo insiste na MESMA chamada — o roteiro repete o suficiente para o
	// detector ter chance de disparar antes de o roteiro acabar.
	insisting := repeatedCall("shell", `{"command":"ls /naoexiste"}`, maxIdenticalToolFailures+2)
	agent, task := newGuardrailAgent(t, &fakeModel{responses: insisting}, failing, journal)
	err := agent.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("o bloqueio não devia devolver erro: %v", err)
	}
	if task.State != domain.StateBlocked {
		t.Fatalf("estado devia ser blocked, veio %s", task.State)
	}
	if task.BlockReason != domain.BlockGuardrail {
		t.Fatalf("motivo devia ser guardrail, veio %s", task.BlockReason)
	}
	if !strings.Contains(task.BlockDetail, "No such file or directory") {
		t.Errorf("o detalhe devia trazer o erro literal: %s", task.BlockDetail)
	}
	// Exatamente três chamadas de ferramenta: a quarta seria desperdício.
	if failing.calls != maxIdenticalToolFailures {
		t.Errorf("esperava %d execuções, houve %d", maxIdenticalToolFailures, failing.calls)
	}
	if !strings.Contains(journal.joined(), "shell") {
		t.Error("o diário devia registrar a ferramenta que falhou")
	}
}

// Ferramenta que FUNCIONA não dispara detector nenhum — o outro sentido.
//
// Sem este caso, um detector quebrado que bloqueasse tudo passaria em todos os
// testes acima.
func TestLoopDoesNotBlockOnHealthyRun(t *testing.T) {
	journal := &recordingJournal{}
	ok := &fakeTool{name: "shell", result: ports.ToolResult{Output: "arquivo1 arquivo2"}}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "1", Name: "shell", Arguments: `{"command":"ls"}`}}},
		{Content: "encontrei dois arquivos", StopReason: "stop"},
	}}
	agent, task := newGuardrailAgent(t, model, ok, journal)
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("execução saudável não devia falhar: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("estado devia ser done, veio %s", task.State)
	}
	if len(journal.lessons) != 0 {
		t.Errorf("execução saudável não devia gerar lição: %v", journal.lessons)
	}
}

// Resposta truncada vira BLOQUEIO, não conclusão.
func TestLoopBlocksOnTruncatedResponse(t *testing.T) {
	journal := &recordingJournal{}
	model := &fakeModel{responses: []ports.Completion{
		{Content: "estava explicando quando fui cort", StopReason: "length"},
	}}
	agent, task := newGuardrailAgent(t, model, nil, journal)
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("não devia devolver erro: %v", err)
	}
	if task.State != domain.StateBlocked {
		t.Fatalf("truncamento devia bloquear, veio %s", task.State)
	}
	if !strings.Contains(task.BlockDetail, "cortada") {
		t.Errorf("o detalhe devia explicar o corte: %s", task.BlockDetail)
	}
}

// O contador de turnos SOBREVIVE e é persistido.
//
// É o buraco que o campo no domínio fecha: antes, o contador nascia em zero a
// cada invocação, e uma tarefa que alternasse bloqueio e retomada ganhava 60
// turnos novos a cada volta.
func TestTurnsAreCountedOnTheTask(t *testing.T) {
	journal := &recordingJournal{}
	ok := &fakeTool{name: "shell", result: ports.ToolResult{Output: "ok"}}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "1", Name: "shell", Arguments: "{}"}}},
		{ToolCalls: []domain.ToolCall{{ID: "2", Name: "shell", Arguments: "{}"}}},
		{Content: "pronto", StopReason: "stop"},
	}}
	agent, task := newGuardrailAgent(t, model, ok, journal)
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("execução falhou: %v", err)
	}
	if task.TurnsUsed != 3 {
		t.Fatalf("esperava 3 turnos contados, veio %d", task.TurnsUsed)
	}
}

// Falha ao gravar a lição NÃO desfaz o bloqueio.
//
// A contenção já aconteceu; perder a lição é menos grave que perder a parada.
func TestBlockSurvivesJournalFailure(t *testing.T) {
	journal := &recordingJournal{failLesson: true}
	failing := &fakeTool{name: "shell", result: ports.ToolResult{Output: "erro", Failed: true}}
	insisting := repeatedCall("shell", "{}", maxIdenticalToolFailures+2)
	agent, task := newGuardrailAgent(t, &fakeModel{responses: insisting}, failing, journal)
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("não devia devolver erro: %v", err)
	}
	if task.State != domain.StateBlocked {
		t.Fatalf("o bloqueio devia acontecer mesmo sem conseguir aprender, veio %s", task.State)
	}
}

// repeatedCall monta um roteiro em que o modelo pede a MESMA coisa n vezes.
//
// O roteiro é maior que o limiar de propósito: se o detector não disparar, o
// teste falha por estado errado em vez de por roteiro curto — a diferença entre
// "o guardrail não funcionou" e "o dublê acabou".
func repeatedCall(tool, arguments string, n int) []ports.Completion {
	out := make([]ports.Completion, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ports.Completion{
			ToolCalls: []domain.ToolCall{{ID: "c", Name: tool, Arguments: arguments}},
		})
	}
	return out
}

// newGuardrailAgent monta o agente com o diário sob observação.
//
// Devolve a tarefa junto porque todo caso deste arquivo asseveram sobre o
// ESTADO dela — bloqueada, concluída, contagem de turnos.
func newGuardrailAgent(t *testing.T, model ports.LanguageModel, tool ports.Tool, journal GuardrailJournal) (*Agent, *domain.Task) {
	t.Helper()
	tools := []ports.Tool{}
	if tool != nil {
		tools = append(tools, tool)
	}
	agent := NewAgent(model, tools, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithGuardrailJournal(journal))
	task, err := domain.NewTask("guardrail-1", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação da tarefa falhou: %v", err)
	}
	return agent, task
}

// A lição aprendida CHEGA ao prompt de sistema da próxima tarefa.
//
// É o teste que separa este trabalho do ralph, e o único que prova o ciclo
// inteiro: detector dispara, lição é gravada, e a tarefa seguinte a recebe sem
// depender de o modelo resolver ler um arquivo.
func TestLessonReachesTheNextTaskPrompt(t *testing.T) {
	journal := &recordingJournal{}
	// Primeira tarefa: a ferramenta falha em laço e o detector aprende.
	failing := &fakeTool{name: "shell", result: ports.ToolResult{Output: "connection refused", Failed: true}}
	first, firstTask := newGuardrailAgent(t, &fakeModel{
		responses: repeatedCall("shell", `{"command":"curl interno"}`, maxIdenticalToolFailures+2),
	}, failing, journal)
	if err := first.Run(context.Background(), firstTask); err != nil {
		t.Fatalf("primeira tarefa: %v", err)
	}
	if len(journal.lessons) == 0 {
		t.Fatal("a primeira tarefa devia ter deixado uma lição")
	}

	// Segunda tarefa: um modelo que só registra o que recebeu.
	spy := &promptSpy{}
	second := NewAgent(spy, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithGuardrailJournal(journal))
	secondTask, err := domain.NewTask("guardrail-2", 1, "outra coisa", fixedClock())
	if err != nil {
		t.Fatalf("criando a segunda tarefa: %v", err)
	}
	if err := second.Run(context.Background(), secondTask); err != nil {
		t.Fatalf("segunda tarefa: %v", err)
	}

	if !strings.Contains(spy.systemSeen, "connection refused") {
		t.Fatalf("a lição devia estar no prompt de sistema da tarefa seguinte:\n%s", spy.systemSeen)
	}
	if !strings.Contains(spy.systemSeen, "instruções") {
		t.Error("a instrução original devia continuar presente")
	}
}

// Sem lição nenhuma, o prompt de sistema fica EXATAMENTE como era.
//
// Sem este caso, um bug que anexasse um bloco vazio passaria — e um bloco vazio
// no prefixo invalida o cache de prompt do fornecedor a cada tarefa.
func TestSystemPromptIsUntouchedWithoutLessons(t *testing.T) {
	spy := &promptSpy{}
	agent := NewAgent(spy, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithGuardrailJournal(&recordingJournal{}))
	task, _ := domain.NewTask("t", 1, "faça", fixedClock())
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("execução: %v", err)
	}
	if spy.systemSeen != "instruções" {
		t.Fatalf("o prompt devia ficar intacto, veio %q", spy.systemSeen)
	}
}

// promptSpy guarda a instrução de sistema que chegou ao modelo.
type promptSpy struct {
	systemSeen string
}

// Complete registra o prompt de sistema e encerra a tarefa sem pedir ferramenta.
func (p *promptSpy) Complete(_ context.Context, messages []domain.Message, _ []ports.ToolSpec) (*ports.Completion, error) {
	for _, m := range messages {
		if m.Role == domain.RoleSystem {
			p.systemSeen = m.Content
			break
		}
	}
	return &ports.Completion{Content: "pronto", StopReason: "stop"}, nil
}

// Erro MUITO longo é cortado antes de virar detalhe de bloqueio.
//
// A saída de uma ferramenta pode ter quilobytes; o detalhe vai para a tela e
// para o aviso, e um texto assim ali é ilegível — quem precisa agir não acha a
// informação no meio do despejo.
func TestToolLoopTruncatesHugeError(t *testing.T) {
	hit := checkToolLoop("shell", "args", strings.Repeat("x", 4000), maxIdenticalToolFailures)
	if hit == nil {
		t.Fatal("devia disparar")
	}
	if len(hit.Detail) > 500 {
		t.Fatalf("o detalhe devia ser cortado, veio com %d bytes", len(hit.Detail))
	}
	if !strings.Contains(hit.Detail, "…") {
		t.Error("o corte devia ser sinalizado")
	}
}

// Ferramenta DESCONHECIDA não entra na contagem de laço.
//
// Nome inventado pelo modelo já é respondido no histórico; contá-lo como falha
// de execução misturaria dois defeitos diferentes na mesma métrica, e três
// nomes errados seguidos bloqueariam uma tarefa que só precisava de correção.
func TestUnknownToolIsNotCountedAsFailure(t *testing.T) {
	agent := NewAgent(&fakeModel{}, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "i", WithGuardrailJournal(&recordingJournal{}))
	guard := newGuardrailState(fixedClock(), 0)

	for i := 0; i < 5; i++ {
		out := toolOutcome{call: domain.ToolCall{Name: "inexistente"}, known: false}
		if hit := agent.observeOutcome(context.Background(), guard, out); hit != nil {
			t.Fatalf("ferramenta desconhecida não devia disparar (rodada %d)", i)
		}
	}
}

// Erro de EXECUÇÃO (err != nil) conta como falha, igual a `Failed: true`.
//
// Os dois caminhos existem no laço e significam a mesma coisa para o detector:
// a chamada não deu certo. Contar só um deixaria metade do laço invisível.
func TestExecutionErrorCountsAsFailure(t *testing.T) {
	if !guardrailToolResultFailed(nil, errors.New("processo morreu")) {
		t.Error("erro de execução devia contar como falha")
	}
	if !guardrailToolResultFailed(&ports.ToolResult{Failed: true}, nil) {
		t.Error("Failed:true devia contar como falha")
	}
	if guardrailToolResultFailed(&ports.ToolResult{Output: "ok"}, nil) {
		t.Error("resultado bom não é falha")
	}
	if guardrailToolResultFailed(nil, nil) {
		t.Error("resultado nulo sem erro não devia contar")
	}
}

// Bloquear uma tarefa que já saiu de `running` devolve erro em vez de silêncio.
//
// O detector pode chegar tarde — outra ferramenta do mesmo turno já pediu
// take-over. Engolir isso esconderia um defeito de ordenação no laço.
func TestApplyHitOnNonRunningTaskReturnsError(t *testing.T) {
	agent := NewAgent(&fakeModel{}, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "i", WithGuardrailJournal(&recordingJournal{}))
	task := &domain.Task{ID: "t", Screen: 1, State: domain.StateDone}
	conv := domain.NewConversation("t", "i")

	err := agent.applyHit(context.Background(), task, conv,
		&GuardrailHit{Kind: GuardrailTurnCap, Detail: "teto"})
	if err == nil {
		t.Fatal("bloquear tarefa concluída devia falhar")
	}
	if !strings.Contains(err.Error(), string(GuardrailTurnCap)) {
		t.Errorf("o erro devia nomear o detector: %v", err)
	}
}

// A variável de ambiente ajusta o limiar, e valor inválido cai no padrão.
//
// O segundo caso é o que importa: um teto malformado não pode derrubar o
// agente nem, pior, desligar o detector — cair no padrão é o comportamento
// seguro.
func TestThresholdFromEnvironmentFallsBackWhenInvalid(t *testing.T) {
	cases := []struct {
		value    string
		expected int
	}{
		{"5", 5},
		{"", 42},
		{"zero", 42},
		{"-3", 42},
		{"0", 42},
		{"  7  ", 7},
	}
	for _, caso := range cases {
		t.Setenv("AGENTD_TESTE_LIMIAR", caso.value)
		if got := envInt("AGENTD_TESTE_LIMIAR", 42); got != caso.expected {
			t.Errorf("envInt(%q) = %d, esperava %d", caso.value, got, caso.expected)
		}
	}
}

// TODO desfecho vai para o progresso: concluída, falhada e bloqueada.
//
// Este teste existe por um furo que eu mesmo abri: `RecordProgress` foi escrito,
// testado no adaptador, e NENHUM caminho de produção o chamava. O arquivo
// existia na máquina com 0 bytes — exatamente o defeito do ralph que este
// trabalho critica, reintroduzido por descuido de fiação.
//
// Testar o método isolado não pega isso. Só testar que o LAÇO o chama pega.
func TestEveryOutcomeIsRecordedInProgress(t *testing.T) {
	cases := []struct {
		name     string
		model    ports.LanguageModel
		tool     ports.Tool
		expected string
	}{
		{
			name:     "concluída",
			model:    &fakeModel{responses: []ports.Completion{{Content: "terminei bem", StopReason: "stop"}}},
			expected: "estado=done",
		},
		{
			name:     "bloqueada por guardrail",
			model:    &fakeModel{responses: repeatedCall("shell", "{}", maxIdenticalToolFailures+2)},
			tool:     &fakeTool{name: "shell", result: ports.ToolResult{Output: "erro", Failed: true}},
			expected: "estado=blocked",
		},
		{
			name:     "falhada pelo modelo",
			model:    &fakeModel{err: errors.New("api fora do ar")},
			expected: "estado=failed",
		},
	}
	for _, caso := range cases {
		t.Run(caso.name, func(t *testing.T) {
			journal := &recordingJournal{}
			agent, task := newGuardrailAgent(t, caso.model, caso.tool, journal)
			_ = agent.Run(context.Background(), task)

			journal.mu.Lock()
			progress := strings.Join(journal.progress, "\n")
			journal.mu.Unlock()

			if progress == "" {
				t.Fatal("o desfecho não foi registrado no progresso")
			}
			if !strings.Contains(progress, caso.expected) {
				t.Errorf("esperava %q no progresso, veio: %s", caso.expected, progress)
			}
			if !strings.Contains(progress, task.ID) {
				t.Errorf("o id da tarefa devia estar no progresso: %s", progress)
			}
		})
	}
}

// fixedCost cobra um valor conhecido por turno, para a conta ser conferível.
type fixedCost struct {
	perTurn float64
	// priced diz se o modelo tem preço; false exercita o caminho "não sei".
	priced bool
}

// Cost devolve o valor combinado.
func (f fixedCost) Cost(string, int, int, int) (float64, bool) {
	return f.perTurn, f.priced
}

// O teto de custo BLOQUEIA quando a soma dos turnos passa do limite.
//
// Com US$ 1,20 por turno e teto de 3,00, o terceiro turno leva a conta a 3,60 e
// a tarefa para. O segundo (2,40) ainda passa — é a fronteira que o teste fixa.
func TestCostCapBlocksWhenAccumulatedCostCrossesLimit(t *testing.T) {
	journal := &recordingJournal{}
	tool := &fakeTool{name: "shell", result: ports.ToolResult{Output: "ok"}}
	model := &fakeModel{responses: repeatedCall("shell", "{}", 10)}

	agent := NewAgent(model, []ports.Tool{tool}, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções",
		WithGuardrailJournal(journal),
		WithCostEstimator(fixedCost{perTurn: 1.20, priced: true}, "modelo-de-teste"))
	task, err := domain.NewTask("custo-1", 1, "gaste", fixedClock())
	if err != nil {
		t.Fatalf("criação: %v", err)
	}

	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("o bloqueio não devia devolver erro: %v", err)
	}
	if task.State != domain.StateBlocked {
		t.Fatalf("devia bloquear por custo, veio %s", task.State)
	}
	if task.BlockReason != domain.BlockGuardrail {
		t.Fatalf("motivo errado: %s", task.BlockReason)
	}
	if !strings.Contains(task.BlockDetail, "US$") {
		t.Errorf("o detalhe devia trazer o valor em dólares: %s", task.BlockDetail)
	}
	// Três turnos: 1,20 + 1,20 + 1,20 = 3,60, e o teto é 3,00.
	if task.TurnsUsed != 3 {
		t.Errorf("esperava parar no 3º turno, parou no %dº", task.TurnsUsed)
	}
	if task.CostUSD < costCapUSD {
		t.Errorf("o custo acumulado devia ter passado do teto: %.2f", task.CostUSD)
	}
}

// Custo BAIXO não bloqueia — o outro sentido.
//
// Sem este caso, um teto quebrado que barrasse toda tarefa passaria no de cima.
func TestCheapTaskIsNotBlockedByCostCap(t *testing.T) {
	journal := &recordingJournal{}
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}

	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções",
		WithGuardrailJournal(journal),
		WithCostEstimator(fixedCost{perTurn: 0.001, priced: true}, "modelo-de-teste"))
	task, _ := domain.NewTask("custo-2", 1, "barato", fixedClock())

	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("execução: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("tarefa barata devia concluir, veio %s", task.State)
	}
	if task.CostUSD <= 0 {
		t.Error("o custo devia ter sido contabilizado mesmo sem bloquear")
	}
}

// Modelo SEM preço não bloqueia, mas os tokens continuam somados.
//
// É a distinção entre "de graça" e "não sei". Sem ela, cadastrar um modelo novo
// e esquecer o preço dele desligaria o teto em silêncio — e o consumo ficaria
// invisível junto, o que impede até descobrir o erro depois.
func TestUnpricedModelStillAccumulatesTokens(t *testing.T) {
	journal := &recordingJournal{}
	model := &fakeModel{responses: []ports.Completion{
		{Content: "pronto", StopReason: "stop", PromptTokens: 5000, CompletionTokens: 700},
	}}

	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções",
		WithGuardrailJournal(journal),
		WithCostEstimator(fixedCost{perTurn: 999.0, priced: false}, "modelo-sem-preco"))
	task, _ := domain.NewTask("custo-3", 1, "sem preço", fixedClock())

	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("execução: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("sem preço não se bloqueia por custo, veio %s", task.State)
	}
	if task.CostUSD != 0 {
		t.Errorf("sem preço o custo fica zero: %.4f", task.CostUSD)
	}
	if task.PromptTokens != 5000 || task.CompletionTokens != 700 {
		t.Errorf("os tokens deviam ser somados mesmo sem preço: %d/%d",
			task.PromptTokens, task.CompletionTokens)
	}
}

// Sem estimador configurado, o teto em dólar simplesmente não existe.
//
// É o comportamento de antes desta opção, e precisa continuar valendo: o CLI e
// qualquer instalação sem tabela de preços rodam igual.
func TestWithoutEstimatorThereIsNoCostCap(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{
		{Content: "pronto", StopReason: "stop", PromptTokens: 9_000_000},
	}}
	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithGuardrailJournal(&recordingJournal{}))
	task, _ := domain.NewTask("custo-4", 1, "sem estimador", fixedClock())

	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("execução: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("sem estimador devia concluir, veio %s", task.State)
	}
}

// O teto em dólar vem do ambiente, e valor inválido cai no padrão.
func TestCostCapFromEnvironmentFallsBackWhenInvalid(t *testing.T) {
	cases := []struct {
		value    string
		expected float64
	}{
		{"1.50", 1.50},
		{"", 3.0},
		{"muito", 3.0},
		{"-2", 3.0},
		{"0", 3.0},
	}
	for _, c := range cases {
		t.Setenv("AGENTD_TESTE_CUSTO", c.value)
		if got := envFloat("AGENTD_TESTE_CUSTO", 3.0); got != c.expected {
			t.Errorf("envFloat(%q) = %.2f, esperava %.2f", c.value, got, c.expected)
		}
	}
}

// O valor em dólares é legível nas duas ordens de grandeza.
//
// Duas casas fixas mostrariam "US$ 0.00" para o que este agente de fato gasta —
// e essa é a frase que a pessoa lê na tela ao ver a tarefa parada.
func TestCostIsReadableAtBothScales(t *testing.T) {
	cases := map[float64]string{
		0.0034: "US$ 0.0034",
		0.0005: "US$ 0.0005",
		3.00:   "US$ 3.00",
		12.5:   "US$ 12.50",
		0:      "US$ 0.0000",
	}
	for value, expected := range cases {
		if got := formatUSD(value); got != expected {
			t.Errorf("formatUSD(%v) = %q, esperava %q", value, got, expected)
		}
	}
}

// O segredo rastreado SOME da saída de ferramenta que o histórico guarda.
//
// É o teste que arma o mecanismo: `Redact` existia inteiro e percorria uma
// lista vazia em toda mensagem, porque `TrackSecret` só era chamado por teste.
// O caminho de produção nunca redigiu nada.
//
// O valor de teste NÃO imita formato de credencial real de propósito: o gate de
// segurança do repositório barra literal com cara de token, e ele está certo em
// barrar — não tem como distinguir exemplo de vazamento.
func TestTrackedSecretIsRedactedFromToolOutput(t *testing.T) {
	const secret = "VALOR-SIGILOSO-DE-TESTE-1234"
	leaking := &fakeTool{
		name:   "shell",
		result: ports.ToolResult{Output: "TOKEN_DO_CONECTOR=" + secret + " e mais coisa"},
	}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "1", Name: "shell", Arguments: `{"command":"env"}`}}},
		{Content: "pronto", StopReason: "stop"},
	}}
	store := newFakeStore()
	agent := NewAgent(model, []ports.Tool{leaking}, &fakeScreen{}, store, &fakeLock{},
		fixedClock, "instruções",
		WithGuardrailJournal(&recordingJournal{}),
		WithTrackedSecrets([]string{secret}))
	task, _ := domain.NewTask("redacao-1", 1, "mostre o ambiente", fixedClock())

	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("execução: %v", err)
	}

	conv, err := store.LoadConversation(context.Background(), task.ID)
	if err != nil || conv == nil {
		t.Fatalf("conversa não foi gravada: %v", err)
	}
	for _, m := range conv.Messages {
		if strings.Contains(m.Content, secret) {
			t.Fatalf("o segredo sobreviveu no histórico (papel %s): %s", m.Role, m.Content)
		}
	}
	// A saída precisa CONTINUAR útil: redigir tudo seria tão ruim quanto não
	// redigir nada, porque o modelo perderia o contexto do que rodou.
	var kept bool
	for _, m := range conv.Messages {
		if strings.Contains(m.Content, "e mais coisa") {
			kept = true
		}
	}
	if !kept {
		t.Error("só o segredo devia sumir, não a saída inteira")
	}
}

// SEM rastrear, o valor passa — é o estado que existia antes.
//
// Este caso documenta o defeito e impede que ele volte por omissão: se alguém
// remover o `WithTrackedSecrets` do ponto de composição, o outro teste falha e
// este continua passando, deixando claro qual metade quebrou.
func TestUntrackedSecretIsNotRedacted(t *testing.T) {
	const secret = "VALOR-QUE-NINGUEM-RASTREOU"
	leaking := &fakeTool{name: "shell", result: ports.ToolResult{Output: "X=" + secret}}
	model := &fakeModel{responses: []ports.Completion{
		{ToolCalls: []domain.ToolCall{{ID: "1", Name: "shell", Arguments: "{}"}}},
		{Content: "pronto", StopReason: "stop"},
	}}
	store := newFakeStore()
	agent := NewAgent(model, []ports.Tool{leaking}, &fakeScreen{}, store, &fakeLock{},
		fixedClock, "instruções", WithGuardrailJournal(&recordingJournal{}))
	task, _ := domain.NewTask("redacao-2", 1, "x", fixedClock())
	_ = agent.Run(context.Background(), task)

	conv, _ := store.LoadConversation(context.Background(), task.ID)
	var found bool
	for _, m := range conv.Messages {
		if strings.Contains(m.Content, secret) {
			found = true
		}
	}
	if !found {
		t.Fatal("sem rastrear, o valor devia passar — é o comportamento documentado")
	}
}

// A redação SOBREVIVE à retomada.
//
// A conversa vem do disco e os segredos não vêm junto: o campo é não-exportado
// para não ser serializado. Sem rearmar no `Resume`, a proteção sumiria
// justamente depois de um take-over — que é quando alguém acabou de digitar uma
// senha na tela.
func TestRedactionSurvivesResume(t *testing.T) {
	const secret = "VALOR-QUE-PRECISA-SOBREVIVER"
	store := newFakeStore()
	agent := NewAgent(&fakeModel{responses: []ports.Completion{{Content: "ok", StopReason: "stop"}}},
		nil, &fakeScreen{}, store, &fakeLock{}, fixedClock, "instruções",
		WithGuardrailJournal(&recordingJournal{}),
		WithTrackedSecrets([]string{secret}))

	task, _ := domain.NewTask("redacao-3", 1, "faça", fixedClock())
	conv := domain.NewConversation(task.ID, "instruções")
	conv.AddUser("pedido")
	if err := store.SaveConversation(context.Background(), conv); err != nil {
		t.Fatalf("preparando a conversa: %v", err)
	}
	_ = task.Start(fixedClock())
	_ = task.Block(domain.BlockPassword, "digite a senha", fixedClock())

	if err := agent.Resume(context.Background(), task, "a senha é "+secret); err != nil {
		t.Fatalf("retomada: %v", err)
	}
	resumed, _ := store.LoadConversation(context.Background(), task.ID)
	for _, m := range resumed.Messages {
		if strings.Contains(m.Content, secret) {
			t.Fatalf("o segredo entrou no histórico pela retomada: %s", m.Content)
		}
	}
}
