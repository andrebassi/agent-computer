package domain

import (
	"strings"
	"testing"
)

// Fato só existe quando a tarefa PAROU.
//
// Publicar "estou rodando" a cada iteração encheria o canal de ruído e ensinaria
// quem recebe a ignorar as mensagens — inclusive o pedido de take-over, que é o
// único que trava a tela até alguém agir.
func TestNewTaskEventOnlyForStoppedStates(t *testing.T) {
	cases := []struct {
		state TaskState
		want  TaskEventKind
		ok    bool
	}{
		{StatePending, "", false},
		{StateRunning, "", false},
		{StateBlocked, EventBlocked, true},
		{StateDone, EventFinished, true},
		{StateFailed, EventFailed, true},
	}
	for _, c := range cases {
		t.Run(string(c.state), func(t *testing.T) {
			task := &Task{ID: "t1", Screen: 2, State: c.state, BlockReason: BlockCaptcha}
			event, ok := NewTaskEvent(task, "resumo", baseTime)
			if ok != c.ok {
				t.Fatalf("estado %s: esperava ok=%v, veio %v", c.state, c.ok, ok)
			}
			if ok && event.Kind != c.want {
				t.Fatalf("estado %s: esperava %s, veio %s", c.state, c.want, event.Kind)
			}
		})
	}
}

// Numa falha, o detalhe é o MOTIVO da falha — não o do bloqueio.
//
// Uma tarefa pode ter bloqueado antes e falhado depois; mostrar o motivo velho
// mandaria quem lê investigar o problema errado.
func TestFailedEventCarriesFailureNotBlockDetail(t *testing.T) {
	task := &Task{
		ID: "t1", Screen: 1, State: StateFailed,
		BlockReason: BlockPassword, BlockDetail: "pedia senha",
		Failure: "o modelo caiu",
	}
	event, ok := NewTaskEvent(task, "", baseTime)
	if !ok {
		t.Fatal("falha devia produzir fato")
	}
	if event.Detail != "o modelo caiu" {
		t.Fatalf("o detalhe devia ser o motivo da falha, veio %q", event.Detail)
	}
}

// A mensagem de bloqueio precisa PEDIR ação, e reaproveitar a descrição do
// motivo — o mesmo texto que a tela mostra.
//
// Ler coisas diferentes na tela e no aviso faria duvidar das duas.
func TestBlockedMessageAsksForAction(t *testing.T) {
	task := &Task{ID: "t1", Screen: 3, State: StateBlocked,
		BlockReason: BlockTwoFactor, BlockDetail: "código do app"}
	event, _ := NewTaskEvent(task, "", baseTime)

	message := event.Message()
	if !strings.Contains(message, "PRECISA DE VOCÊ") {
		t.Fatalf("a mensagem devia pedir ação: %q", message)
	}
	if !strings.Contains(message, BlockTwoFactor.Description()) {
		t.Fatalf("devia reaproveitar a descrição do motivo: %q", message)
	}
	if !strings.Contains(message, "código do app") {
		t.Fatalf("o detalhe devia aparecer: %q", message)
	}
	if !strings.Contains(message, "3") {
		t.Fatalf("a tela devia aparecer: %q", message)
	}
}

// Resumo vazio significa vazio: nenhum texto de preenchimento entra no lugar.
func TestMessageWithoutSummaryStaysShort(t *testing.T) {
	cases := []struct {
		name    string
		task    *Task
		summary string
		want    string
	}{
		{"concluída sem resumo", &Task{Screen: 1, State: StateDone}, "", "tela 1 concluiu"},
		{"concluída com resumo", &Task{Screen: 1, State: StateDone}, "achei 3", "tela 1 concluiu: achei 3"},
		{"falha sem motivo", &Task{Screen: 2, State: StateFailed}, "", "tela 2 falhou"},
		{"falha com motivo", &Task{Screen: 2, State: StateFailed, Failure: "sem rede"}, "", "tela 2 falhou: sem rede"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			event, ok := NewTaskEvent(c.task, c.summary, baseTime)
			if !ok {
				t.Fatal("devia produzir fato")
			}
			if got := event.Message(); got != c.want {
				t.Fatalf("esperava %q, veio %q", c.want, got)
			}
		})
	}
}

// Bloqueio sem detalhe não deixa um travessão solto no fim da frase.
func TestBlockedMessageWithoutDetail(t *testing.T) {
	task := &Task{Screen: 1, State: StateBlocked, BlockReason: BlockCaptcha}
	event, _ := NewTaskEvent(task, "", baseTime)
	if strings.HasSuffix(event.Message(), "— ") {
		t.Fatalf("travessão solto no fim: %q", event.Message())
	}
}

// Espécie desconhecida ainda produz texto legível, em vez de string vazia.
//
// Um fato futuro que ninguém lembrou de tratar aqui apareceria como aviso em
// branco — pior que um aviso feio, porque não dá para diagnosticar.
func TestMessageHandlesUnknownKind(t *testing.T) {
	event := TaskEvent{Screen: 4, Kind: TaskEventKind("espécie-nova")}
	if message := event.Message(); !strings.Contains(message, "espécie-nova") {
		t.Fatalf("espécie desconhecida devia aparecer no texto: %q", message)
	}
}
