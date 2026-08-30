package events

import (
	"context"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// noop descarta tudo. É o padrão quando nenhum destino foi configurado.
//
// Existe para o serviço nunca precisar checar nil antes de publicar: um ramo a
// menos para cobrir, e um caminho a menos onde esquecer a checagem produziria
// pânico em produção.
type noop struct{}

// Publish descarta o fato silenciosamente.
func (noop) Publish(context.Context, domain.TaskEvent) error { return nil }

// Noop devolve um destino que descarta tudo.
func Noop() ports.EventSink { return noop{} }

// kindFilter deixa passar só os fatos das espécies listadas.
type kindFilter struct {
	inner ports.EventSink
	kinds map[domain.TaskEventKind]bool
}

// OnlyKinds envolve um destino e filtra por espécie de fato.
//
// Serve para o padrão ser SILENCIOSO no que não pede ação. Uma tarefa que
// terminou bem numa tela que alguém está assistindo não precisa de aviso, e
// avisar de tudo ensina quem recebe a ignorar — inclusive o pedido de take-over,
// que é o único que realmente trava a tela até alguém agir.
func OnlyKinds(inner ports.EventSink, kinds ...domain.TaskEventKind) ports.EventSink {
	set := make(map[domain.TaskEventKind]bool, len(kinds))
	for _, k := range kinds {
		set[k] = true
	}
	return &kindFilter{inner: inner, kinds: set}
}

// Publish repassa o fato só quando a espécie está na lista.
func (f *kindFilter) Publish(ctx context.Context, event domain.TaskEvent) error {
	if !f.kinds[event.Kind] {
		return nil
	}
	return f.inner.Publish(ctx, event)
}

// tee publica em vários destinos.
type tee struct {
	sinks []ports.EventSink
}

// Tee publica em todos os destinos, na ORDEM dada.
//
// A ordem importa na composição: o spool vem primeiro, para o fato já estar em
// disco caso um destino remoto trave ou falhe. E a falha de um não impede os
// outros — o primeiro erro é devolvido para registro, mas todos são tentados.
func Tee(sinks ...ports.EventSink) ports.EventSink { return &tee{sinks: sinks} }

// Publish tenta todos os destinos e devolve o primeiro erro encontrado.
func (t *tee) Publish(ctx context.Context, event domain.TaskEvent) error {
	var firstErr error
	for _, sink := range t.sinks {
		if err := sink.Publish(ctx, event); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
