package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// baseTime é um instante fixo: teste que usa time.Now() compara valores que
// mudam a cada execução e falha de forma intermitente.
var baseTime = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

// runningTask devolve uma tarefa já iniciada, que é o ponto de partida da
// maioria dos casos abaixo.
func runningTask(t *testing.T) *Task {
	t.Helper()
	task, err := NewTask("t1", 1, "faz algo", baseTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := task.Start(baseTime); err != nil {
		t.Fatalf("start falhou: %v", err)
	}
	return task
}

// Confere que entrada inválida é recusada na criação, e não mais tarde: uma
// tela fora do intervalo só apareceria como serviço systemd que não sobe.
func TestNewTaskRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		screen int
		prompt string
	}{
		{"id vazio", "", 1, "faz algo"},
		{"prompt vazio", "t1", 1, ""},
		{"tela zero", "t1", 0, "faz algo"},
		{"tela negativa", "t1", -1, "faz algo"},
		{"tela acima do limite", "t1", 10, "faz algo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewTask(c.id, c.screen, c.prompt, baseTime); err == nil {
				t.Fatalf("esperava erro para %s, veio nil", c.name)
			}
		})
	}
}

// As telas 1 e 9 são os extremos válidos. Errar o operador de comparação é o
// defeito clássico aqui, e só um teste nas bordas o pega.
func TestNewTaskAcceptsScreenBoundaries(t *testing.T) {
	for _, screen := range []int{1, 9} {
		if _, err := NewTask("t1", screen, "faz algo", baseTime); err != nil {
			t.Fatalf("tela %d devia ser aceita: %v", screen, err)
		}
	}
}

// Percorre o caminho feliz inteiro, conferindo também quando a tarefa deixa de
// ocupar a tela.
func TestTaskLifecycleHappyPath(t *testing.T) {
	task, err := NewTask("t1", 1, "faz algo", baseTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if task.State != StatePending {
		t.Fatalf("estado inicial devia ser pending, veio %s", task.State)
	}
	if !task.Active() {
		t.Fatal("tarefa pendente devia contar como ativa")
	}
	if err := task.Start(baseTime.Add(time.Second)); err != nil {
		t.Fatalf("start falhou: %v", err)
	}
	if err := task.Finish(baseTime.Add(2 * time.Second)); err != nil {
		t.Fatalf("finish falhou: %v", err)
	}
	if task.Active() {
		t.Fatal("tarefa concluída não devia contar como ativa")
	}
}

// A regra que dá sentido ao take-over: tarefa bloqueada continua ocupando a
// tela. Se contasse como inativa, outra entraria por cima enquanto a pessoa
// ainda estivesse digitando a senha.
func TestBlockedTaskStaysActive(t *testing.T) {
	task := runningTask(t)
	if err := task.Block(BlockPassword, "digite a senha do painel", baseTime); err != nil {
		t.Fatalf("block falhou: %v", err)
	}
	if !task.Active() {
		t.Fatal("tarefa bloqueada TEM de contar como ativa")
	}
}

// O motivo do bloqueio vem do modelo, e modelo inventa valor. Um motivo
// desconhecido não pode travar a tarefa sem a tela saber o que pedir.
func TestBlockRejectsUnknownReason(t *testing.T) {
	task := runningTask(t)
	err := task.Block(BlockReason("invente_um_motivo"), "detalhe", baseTime)
	if !errors.Is(err, ErrInvalidReason) {
		t.Fatalf("esperava ErrInvalidReason, veio %v", err)
	}
	if task.State != StateRunning {
		t.Fatalf("estado não devia mudar num bloqueio recusado, veio %s", task.State)
	}
}

// Retomar só faz sentido a partir de bloqueado; de qualquer outro estado é
// defeito de quem chamou.
func TestResumeOnlyFromBlocked(t *testing.T) {
	task := runningTask(t)
	if err := task.Resume(baseTime); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resume de tarefa rodando devia falhar, veio %v", err)
	}
}

// Bloqueio que não se limpa ao retomar deixa a tela mostrando "precisa de você"
// com a tarefa já rodando.
func TestResumeClearsBlockFields(t *testing.T) {
	task := runningTask(t)
	if err := task.Block(BlockCaptcha, "resolva o desafio", baseTime); err != nil {
		t.Fatalf("block falhou: %v", err)
	}
	if err := task.Resume(baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("resume falhou: %v", err)
	}
	if task.BlockReason != "" || task.BlockDetail != "" {
		t.Fatalf("campos de bloqueio deviam ser limpos, veio %q/%q", task.BlockReason, task.BlockDetail)
	}
}

// Varre as transições que a máquina de estados precisa recusar. Sem isto, uma
// tarefa concluída poderia voltar a rodar.
func TestInvalidTransitions(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Task) error
	}{
		{"start duas vezes", func(task *Task) error {
			_ = task.Start(baseTime)
			return task.Start(baseTime)
		}},
		{"finish sem start", func(task *Task) error { return task.Finish(baseTime) }},
		{"block sem start", func(task *Task) error { return task.Block(BlockCaptcha, "x", baseTime) }},
		{"finish depois de finish", func(task *Task) error {
			_ = task.Start(baseTime)
			_ = task.Finish(baseTime)
			return task.Finish(baseTime)
		}},
		{"fail depois de finish", func(task *Task) error {
			_ = task.Start(baseTime)
			_ = task.Finish(baseTime)
			return task.Fail("tarde demais", baseTime)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task, err := NewTask("t1", 1, "faz algo", baseTime)
			if err != nil {
				t.Fatalf("criação falhou: %v", err)
			}
			if err := c.run(task); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("esperava ErrInvalidTransition, veio %v", err)
			}
		})
	}
}

// Abandonar uma tarefa que nunca chegou a rodar precisa LIBERAR A TELA.
//
// Ela ocupa a tela mesmo sem ter começado — Active() conta pendente. Enquanto
// Fail recusava vir de pendente, abandoná-la deixava o estado intacto: o comando
// dizia "tela liberada", a tela seguia ocupada, e a próxima tarefa levava "a tela
// já tem uma tarefa ativa" sem explicação.
//
// A asserção que importa é a última: não é o estado que interessa, é a tela.
func TestFailFromPendingFreesTheScreen(t *testing.T) {
	task, err := NewTask("t1", 1, "faz algo", baseTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if !task.Active() {
		t.Fatal("preparação falhou: pendente devia ocupar a tela")
	}
	if err := task.Fail("abandonada antes de começar", baseTime); err != nil {
		t.Fatalf("fail a partir de pendente devia ser aceito: %v", err)
	}
	if task.State != StateFailed || task.Failure == "" {
		t.Fatalf("estado ou motivo errado: %s/%q", task.State, task.Failure)
	}
	if task.Active() {
		t.Fatal("a tela continua ocupada — abandonar não liberou nada")
	}
}

// Abandonar uma tarefa que espera a pessoa é legítimo: ela pode nunca aparecer.
func TestFailFromBlocked(t *testing.T) {
	task := runningTask(t)
	if err := task.Block(BlockTwoFactor, "código", baseTime); err != nil {
		t.Fatalf("block falhou: %v", err)
	}
	if err := task.Fail("humano não respondeu", baseTime); err != nil {
		t.Fatalf("fail a partir de bloqueado devia ser aceito: %v", err)
	}
	if task.State != StateFailed || task.Failure == "" {
		t.Fatalf("estado ou motivo errado: %s/%q", task.State, task.Failure)
	}
}

// Os cinco motivos são os que a documentação lista. Este teste existe para que
// remover um seja decisão consciente, e não descuido de refatoração.
func TestValidBlockReasonCoversDocumentedSet(t *testing.T) {
	documented := []BlockReason{
		BlockPassword, BlockTwoFactor, BlockCaptcha, BlockPaymentIdentity, BlockHumanRequired,
	}
	for _, r := range documented {
		if !ValidBlockReason(r) {
			t.Fatalf("motivo documentado %q devia ser válido", r)
		}
		if got := r.Description(); got == "" || got == "motivo desconhecido" {
			t.Fatalf("motivo %q sem descrição útil: %q", r, got)
		}
	}
	if ValidBlockReason(BlockReason("outro")) {
		t.Fatal("motivo fora da lista não devia ser válido")
	}
	if got := BlockReason("outro").Description(); got != "motivo desconhecido" {
		t.Fatalf("descrição de motivo desconhecido: %q", got)
	}
}

// O motivo `guardrail` é válido, tem descrição própria, e ela NÃO se confunde
// com a dos cinco documentados.
//
// A distinção é o ponto: os cinco descrevem o que o SITE exige; este é nós
// parando o agente. Reaproveitar a descrição de `human_required` faria a tela
// dizer "o site exige uma pessoa" quando o site não exigiu nada — mentira sobre
// a causa, na hora em que alguém precisa entender por que a tarefa parou.
func TestGuardrailReasonIsValidAndDistinct(t *testing.T) {
	if !ValidBlockReason(BlockGuardrail) {
		t.Fatal("o motivo guardrail devia ser válido")
	}
	description := BlockGuardrail.Description()
	if description == "" || description == "motivo desconhecido" {
		t.Fatalf("guardrail sem descrição útil: %q", description)
	}
	for _, documented := range []BlockReason{
		BlockPassword, BlockTwoFactor, BlockCaptcha, BlockPaymentIdentity, BlockHumanRequired,
	} {
		if documented.Description() == description {
			t.Fatalf("guardrail não pode compartilhar a descrição de %q", documented)
		}
	}
}

// Uma tarefa bloqueada por guardrail continua RETOMÁVEL.
//
// É o que separa este bloqueio de uma falha: o trabalho e o histórico ficam, e
// a pessoa decide se retoma. Se o guardrail encerrasse a tarefa, parar cedo
// custaria tudo o que já tinha sido feito.
func TestGuardrailBlockIsResumable(t *testing.T) {
	now := time.Now()
	task, err := NewTask("t-guard", 1, "faça algo", now)
	if err != nil {
		t.Fatalf("criação: %v", err)
	}
	if err := task.Start(now); err != nil {
		t.Fatalf("início: %v", err)
	}
	if err := task.Block(BlockGuardrail, "teto de turnos atingido", now); err != nil {
		t.Fatalf("bloqueio por guardrail devia ser aceito: %v", err)
	}
	if !task.Active() {
		t.Error("tarefa bloqueada continua ocupando a tela")
	}
	if err := task.Resume(now); err != nil {
		t.Fatalf("devia ser retomável: %v", err)
	}
	if task.State != StateRunning {
		t.Fatalf("depois de retomar devia estar running, veio %s", task.State)
	}
	if task.BlockReason != "" {
		t.Errorf("o motivo devia ser limpo na retomada, ficou %q", task.BlockReason)
	}
}

// A tela precisa mostrar algo em TODO estado. Um estado sem texto viraria uma
// faixa em branco, indistinguível de overlay quebrado.
func TestStatusLineForEveryState(t *testing.T) {
	states := []TaskState{StatePending, StateRunning, StateBlocked, StateDone, StateFailed, TaskState("estranho")}
	for _, st := range states {
		task := &Task{Screen: 3, State: st, BlockReason: BlockCaptcha, Failure: "algo"}
		line := task.StatusLine()
		if line == "" {
			t.Fatalf("estado %s sem linha de status", st)
		}
		if !strings.Contains(line, "tela 3") {
			t.Fatalf("linha de status sem o número da tela: %q", line)
		}
	}
}

// É a linha que faz a pessoa olhar para a tela. Se deixar de chamar por
// atenção, o take-over para de funcionar na prática.
func TestBlockedStatusLineCallsForHuman(t *testing.T) {
	task := runningTask(t)
	if err := task.Block(BlockPassword, "senha do painel", baseTime); err != nil {
		t.Fatalf("block falhou: %v", err)
	}
	line := task.StatusLine()
	if !strings.Contains(line, "PRECISA DE VOCÊ") {
		t.Fatalf("linha de bloqueio devia chamar a pessoa: %q", line)
	}
	if !strings.Contains(line, BlockPassword.Description()) {
		t.Fatalf("linha de bloqueio devia dizer o motivo: %q", line)
	}
}
