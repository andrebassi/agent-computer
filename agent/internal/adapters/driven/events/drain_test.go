package events

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// spoolWith prepara uma fila já com os fatos dados.
func spoolWith(t *testing.T, kinds ...domain.TaskEventKind) *Spool {
	t.Helper()
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	for i, kind := range kinds {
		if err := spool.Publish(context.Background(), newEvent(string(rune('a'+i)), kind)); err != nil {
			t.Fatalf("preparação falhou: %v", err)
		}
	}
	return spool
}

// breakSpoolFile torna a fila ilegível, pondo um diretório no lugar do arquivo.
func breakSpoolFile(t *testing.T, spool *Spool) {
	t.Helper()
	_ = os.Remove(spool.Path())
	if err := os.MkdirAll(spool.Path(), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
}

// Drenar entrega tudo e esvazia a fila.
func TestDrainDeliversAndClears(t *testing.T) {
	spool := spoolWith(t, domain.EventBlocked, domain.EventFailed, domain.EventFinished)
	sink := &recorder{}

	delivered, err := Drain(context.Background(), spool, sink)
	if err != nil {
		t.Fatalf("Drain falhou: %v", err)
	}
	if delivered != 3 {
		t.Fatalf("esperava 3 entregues, veio %d", delivered)
	}
	pending, _ := spool.Pending(context.Background())
	if len(pending) != 0 {
		t.Fatalf("a fila devia estar vazia, tem %d", len(pending))
	}
}

// Falha na entrega MANTÉM a fila intacta.
//
// Limpar depois de entrega parcial perderia os fatos restantes, e o mais
// provável de falhar é o mais recente — que costuma ser o mais urgente. A
// consequência aceita é a oposta: repetir um aviso. Repetido incomoda; perdido
// deixa uma tela travada sem ninguém saber.
func TestDrainKeepsQueueWhenDeliveryFails(t *testing.T) {
	spool := spoolWith(t, domain.EventBlocked, domain.EventFailed)
	sink := &recorder{err: errors.New("sem rede")}

	delivered, err := Drain(context.Background(), spool, sink)
	if err == nil {
		t.Fatal("falha de entrega devia ser reportada")
	}
	if delivered != 0 {
		t.Fatalf("nada devia ter sido entregue, veio %d", delivered)
	}
	pending, _ := spool.Pending(context.Background())
	if len(pending) != 2 {
		t.Fatalf("a fila devia continuar intacta, tem %d", len(pending))
	}
}

// Fila vazia é silenciosa — o caso mais comum com o timer rodando a cada minuto.
func TestDrainOnEmptyQueueIsQuiet(t *testing.T) {
	spool := spoolWith(t)
	sink := &recorder{}

	delivered, err := Drain(context.Background(), spool, sink)
	if err != nil || delivered != 0 {
		t.Fatalf("fila vazia devia ser silenciosa: %v / %d", err, delivered)
	}
	if len(sink.got) != 0 {
		t.Fatal("nada devia ter sido publicado")
	}
}

// Fila ilegível é reportada, em vez de tratada como vazia.
func TestDrainReportsUnreadableQueue(t *testing.T) {
	spool := spoolWith(t)
	breakSpoolFile(t, spool)
	if _, err := Drain(context.Background(), spool, &recorder{}); err == nil {
		t.Fatal("fila ilegível devia ser reportada")
	}
}

// Falha ao limpar depois de entregar tudo também é reportada.
//
// Sem isso, a fila voltaria a ser drenada na passada seguinte e todo aviso seria
// entregue de novo, indefinidamente.
func TestDrainReportsClearFailure(t *testing.T) {
	spool := spoolWith(t, domain.EventBlocked)
	pending, _ := spool.Pending(context.Background())
	if len(pending) != 1 {
		t.Fatalf("preparação falhou: %d eventos", len(pending))
	}
	breakSpoolFile(t, spool)

	if _, err := Drain(context.Background(), spool, &recorder{}); err == nil {
		t.Fatal("falha ao limpar devia ser reportada")
	}
}

// Describe rende as linhas humanas, para o drenador ser útil rodado à mão.
func TestDescribeRendersHumanLines(t *testing.T) {
	events := []domain.TaskEvent{
		{Screen: 1, Kind: domain.EventBlocked, Reason: domain.BlockCaptcha},
		{Screen: 2, Kind: domain.EventFinished, Summary: "achei 3"},
	}
	lines := Describe(events)
	if len(lines) != 2 {
		t.Fatalf("esperava 2 linhas, veio %d", len(lines))
	}
	if !strings.Contains(lines[0], "PRECISA DE VOCÊ") {
		t.Fatalf("a linha de bloqueio devia pedir ação: %q", lines[0])
	}
}
