package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// Os testes deste arquivo exercitam a VERIFICAÇÃO DENTRO DO LAÇO.
//
// `verify_test.go` cobre as peças isoladas — o default mudo, o teto, a redação
// da lição. Isso não prova o que interessa: que a tarefa muda de desfecho. Um
// verificador perfeito ligado num laço que ignora o veredicto passaria em todos
// aqueles testes e não faria nada.

// TestLoopFinishesWhenVerdictIsMet trava o caminho feliz.
//
// Com veredicto positivo o laço tem de terminar na primeira parada, exatamente
// como antes da verificação existir — senão ligar o verificador custaria um
// turno extra em toda tarefa que já estava certa.
func TestLoopFinishesWhenVerdictIsMet(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	verifier := &stubVerifier{verdicts: []Verdict{{Met: true, Evidence: "arquivo gravado"}}}
	agent := NewAgent(model, nil, screen, store, lock, fixedClock, "instruções",
		WithVerifier(verifier))

	task, err := domain.NewTask("t-met", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("estado = %s, want done", task.State)
	}
	if verifier.calls != 1 {
		t.Errorf("verificações = %d, want 1", verifier.calls)
	}
	// O pedido tem de vir de `task.Prompt`, e não da conversa: `Trim` e
	// `Compact` podam o histórico, então numa tarefa longa a mensagem original
	// pode não estar mais lá — e o verificador julgaria contra outro pedido.
	if len(verifier.requests) > 0 && verifier.requests[0] != "faça algo" {
		t.Errorf("o verificador recebeu %q, e não o prompt da tarefa", verifier.requests[0])
	}
}

// TestLoopReturnsTheGapAndThenFinishes é o caminho que justifica o desenho.
//
// A primeira verificação recusa, a lacuna volta ao modelo, ele responde de novo,
// a segunda aprova. Bloquear já na primeira recusa gastaria uma pessoa para algo
// que o modelo resolve sozinho ao ser lembrado.
func TestLoopReturnsTheGapAndThenFinishes(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{
		{Content: "acho que terminei", StopReason: "stop"},
		{Content: "agora sim, gravei o arquivo", StopReason: "stop"},
	}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	verifier := &stubVerifier{verdicts: []Verdict{
		{Met: false, Missing: "falta gravar o arquivo em /tmp/saida"},
		{Met: true, Evidence: "o arquivo aparece no turno seguinte"},
	}}
	agent := NewAgent(model, nil, screen, store, lock, fixedClock, "instruções",
		WithVerifier(verifier))

	task, err := domain.NewTask("t-gap", 1, "grave o arquivo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if task.State != domain.StateDone {
		t.Fatalf("estado = %s, want done", task.State)
	}
	if verifier.calls != 2 {
		t.Fatalf("verificações = %d, want 2 — a lacuna não foi devolvida", verifier.calls)
	}
	// A lacuna precisa chegar ao MODELO, não só ao registro: é ela que faz o
	// turno seguinte corrigir em vez de repetir.
	if model.calls < 2 {
		t.Errorf("o modelo foi chamado %d vez(es); a devolução não gerou novo turno", model.calls)
	}
}

// TestLoopBlocksWhenVerificationIsExhausted cobre o outro desfecho.
//
// Com o teto em zero não há devolução e a primeira recusa vira bloqueio. Forçar
// o teto pela variável é o que torna este teste determinístico — pedir ao modelo
// que falhe é a armadilha registrada no README: ele coopera de menos, e o teste
// passa a reprovar de forma intermitente.
func TestLoopBlocksWhenVerificationIsExhausted(t *testing.T) {
	t.Setenv("AGENTD_MAX_VERIFY_ATTEMPTS", "0")
	model := &fakeModel{responses: []ports.Completion{{Content: "terminei", StopReason: "stop"}}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	verifier := &stubVerifier{verdicts: []Verdict{
		{Met: false, Missing: "o relatório não foi gerado", Evidence: "nenhuma escrita no histórico"},
	}}
	agent := NewAgent(model, nil, screen, store, lock, fixedClock, "instruções",
		WithVerifier(verifier))

	task, err := domain.NewTask("t-block", 1, "gere o relatório", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if task.State != domain.StateBlocked {
		t.Fatalf("estado = %s, want blocked", task.State)
	}
	// O motivo precisa ser o novo, e não `guardrail`: os dois são nossos, mas
	// dizem coisas opostas — um contém quem foi longe demais, o outro marca
	// quem parou cedo demais, e a tela pergunta coisas diferentes em cada caso.
	if task.BlockReason != domain.BlockUnverified {
		t.Errorf("motivo = %q, want %q", task.BlockReason, domain.BlockUnverified)
	}
	if !strings.Contains(task.BlockDetail, "o relatório não foi gerado") {
		t.Errorf("o detalhe não diz o que faltou: %q", task.BlockDetail)
	}
	// A tela liberada importa tanto quanto o bloqueio: tarefa bloqueada que
	// segura a trava deixa a tela inutilizável até alguém notar.
	if lock.released != lock.acquired || lock.acquired == 0 {
		t.Errorf("trava: %d adquiridas, %d liberadas", lock.acquired, lock.released)
	}
}

// TestLoopIgnoresVerifierFailure trava a abstenção dentro do laço.
//
// Verificação indisponível não pode reprovar: uma falha de rede transformaria
// toda tarefa boa em bloqueio, e isso só apareceria com o backend já fora do ar
// — o pior momento possível para descobrir.
func TestLoopIgnoresVerifierFailure(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	verifier := &stubVerifier{err: errors.New("backend fora do ar")}
	agent := NewAgent(model, nil, screen, store, lock, fixedClock, "instruções",
		WithVerifier(verifier))

	task, err := domain.NewTask("t-err", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if task.State != domain.StateDone {
		t.Errorf("estado = %s, want done — erro de verificação virou reprovação", task.State)
	}
}

// TestLoopWithoutVerifierKeepsOldBehavior é a prova de que nada mudou para quem
// não liga a verificação.
//
// É o teste que protege contra a regressão mais cara desta funcionalidade:
// alguém atualiza o binário e as tarefas passam a bloquear ou a custar um turno
// a mais, sem ter pedido nada.
func TestLoopWithoutVerifierKeepsOldBehavior(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	store, lock, screen := newFakeStore(), &fakeLock{}, &fakeScreen{}
	agent := NewAgent(model, nil, screen, store, lock, fixedClock, "instruções")

	task, err := domain.NewTask("t-default", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("Run falhou: %v", err)
	}
	if task.State != domain.StateDone {
		t.Errorf("estado = %s, want done", task.State)
	}
	if model.calls != 1 {
		t.Errorf("chamadas = %d, want 1 — o default cobrou turno extra", model.calls)
	}
}
