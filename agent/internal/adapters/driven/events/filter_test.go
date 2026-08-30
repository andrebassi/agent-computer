package events

import (
	"context"
	"errors"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// recorder guarda o que recebeu, e pode falhar sob comando.
type recorder struct {
	got []domain.TaskEvent
	err error
}

// Publish registra o fato ou devolve o erro configurado.
func (r *recorder) Publish(_ context.Context, event domain.TaskEvent) error {
	if r.err != nil {
		return r.err
	}
	r.got = append(r.got, event)
	return nil
}

// O filtro deixa passar só o que pede ação.
//
// Avisar de tudo ensina quem recebe a ignorar — inclusive o pedido de
// take-over, que é o único que trava a tela até alguém agir.
func TestOnlyKindsLetsThroughWhatItShould(t *testing.T) {
	inner := &recorder{}
	sink := OnlyKinds(inner, domain.EventBlocked, domain.EventFailed)
	ctx := context.Background()

	for _, kind := range []domain.TaskEventKind{
		domain.EventBlocked, domain.EventFinished, domain.EventFailed, domain.EventFinished,
	} {
		if err := sink.Publish(ctx, domain.TaskEvent{Kind: kind}); err != nil {
			t.Fatalf("Publish falhou: %v", err)
		}
	}

	if len(inner.got) != 2 {
		t.Fatalf("esperava 2 fatos (bloqueio e falha), veio %d", len(inner.got))
	}
	if inner.got[0].Kind != domain.EventBlocked || inner.got[1].Kind != domain.EventFailed {
		t.Fatalf("passou a espécie errada: %v", inner.got)
	}
}

// Filtro sem espécie nenhuma não deixa passar nada — silêncio total.
func TestOnlyKindsWithNoKindsBlocksEverything(t *testing.T) {
	inner := &recorder{}
	sink := OnlyKinds(inner)
	if err := sink.Publish(context.Background(), domain.TaskEvent{Kind: domain.EventBlocked}); err != nil {
		t.Fatalf("Publish falhou: %v", err)
	}
	if len(inner.got) != 0 {
		t.Fatalf("nada devia passar, veio %d", len(inner.got))
	}
}

// Tee publica em TODOS, e a falha de um não impede os outros.
//
// O spool vem primeiro na composição justamente por isso: se o destino remoto
// travar, o fato já está em disco e o drenador o entrega depois.
func TestTeePublishesToAllEvenWhenOneFails(t *testing.T) {
	first := &recorder{}
	broken := &recorder{err: errors.New("sem rede")}
	last := &recorder{}

	sink := Tee(first, broken, last)
	err := sink.Publish(context.Background(), domain.TaskEvent{Kind: domain.EventBlocked})

	if err == nil {
		t.Fatal("o erro devia ser devolvido para registro")
	}
	if len(first.got) != 1 {
		t.Fatal("o destino anterior ao que falhou devia ter recebido")
	}
	if len(last.got) != 1 {
		t.Fatal("o destino POSTERIOR ao que falhou também devia ter recebido")
	}
}

// Sem destino nenhum, Tee não faz nada e não falha.
func TestTeeWithNoSinksIsHarmless(t *testing.T) {
	if err := Tee().Publish(context.Background(), domain.TaskEvent{}); err != nil {
		t.Fatalf("Tee vazio não devia falhar: %v", err)
	}
}

// O destino nulo descarta e nunca falha.
func TestNoopDiscardsSilently(t *testing.T) {
	if err := Noop().Publish(context.Background(), domain.TaskEvent{Kind: domain.EventBlocked}); err != nil {
		t.Fatalf("Noop não devia falhar: %v", err)
	}
}
