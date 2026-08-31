package events

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// destinationSeparator separa os destinos em AGENT_WEBHOOK.
const destinationSeparator = ","

// MultiSink entrega o mesmo aviso a vários destinos.
//
// Existe porque os destinos servem a leitores diferentes: o ntfy entrega TEXTO
// ao celular de quem precisa agir, e um coletor de webhook guarda o JSON CRU
// com os campos, que é o que serve para depurar. Obrigar a escolher um faria
// perder o outro.
type MultiSink struct {
	sinks []*Webhook
	// urls guarda o destino de cada sink, para a mensagem de erro dizer QUAL
	// falhou — "a entrega falhou" com dois destinos configurados não ajuda.
	urls []string
}

// ParseDestinations lê a lista de destinos de uma string de configuração.
//
// Formato de cada item, separados por vírgula:
//
//	<formato>=<url>   escolhe o formato daquele destino
//	<url>             usa o formato padrão recebido em fallback
//
// O prefixo só é reconhecido quando o que vem antes do `=` É um formato
// conhecido. Sem essa checagem, uma URL com query string (`.../in/?a=b`) teria
// o "https://…?a" tomado por nome de formato — e o destino inteiro viraria lixo
// silencioso.
func ParseDestinations(raw string, fallback WebhookFormat) ([]string, []WebhookFormat) {
	urls := make([]string, 0, 2)
	formats := make([]WebhookFormat, 0, 2)

	for _, item := range strings.Split(raw, destinationSeparator) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		format := fallback
		if prefix, rest, found := strings.Cut(item, "="); found && isKnownFormat(prefix) {
			format = ParseWebhookFormat(prefix)
			item = strings.TrimSpace(rest)
		}
		if item == "" {
			continue
		}
		urls = append(urls, item)
		formats = append(formats, format)
	}
	return urls, formats
}

// isKnownFormat diz se o texto nomeia um formato, e não parte de uma URL.
func isKnownFormat(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case string(FormatNtfy), string(FormatRaw):
		return true
	}
	return false
}

// NewMultiSink monta os destinos a partir da configuração.
//
// Um único destino continua produzindo o comportamento de sempre: a lista de um
// elemento se comporta como o Webhook simples, e nada do que já funcionava muda.
func NewMultiSink(raw string, fallback WebhookFormat) (*MultiSink, error) {
	urls, formats := ParseDestinations(raw, fallback)
	if len(urls) == 0 {
		return nil, fmt.Errorf("nenhum destino de aviso configurado")
	}
	multi := &MultiSink{sinks: make([]*Webhook, 0, len(urls)), urls: urls}
	for i, url := range urls {
		hook, err := NewWebhook(url)
		if err != nil {
			return nil, fmt.Errorf("destino %d: %w", i+1, err)
		}
		multi.sinks = append(multi.sinks, hook.WithFormat(formats[i]))
	}
	return multi, nil
}

// Destinations devolve quantos destinos existem, para o diagnóstico.
func (m *MultiSink) Destinations() int { return len(m.sinks) }

// Publish entrega a todos e considera sucesso se PELO MENOS UM aceitar.
//
// A alternativa — exigir que todos aceitem — parece mais rigorosa e é pior na
// prática: um destino permanentemente quebrado seguraria o aviso na fila, e a
// cada passada do timer o destino BOM receberia a mesma notificação de novo.
// Quem recebe silencia o canal, e aí nada mais chega.
//
// O objetivo do mecanismo é a pessoa ficar sabendo. Se um destino entregou, ela
// soube — o que falhou vira erro no log, sem travar a fila.
func (m *MultiSink) Publish(ctx context.Context, event domain.TaskEvent) error {
	var failures []string
	delivered := 0

	for i, sink := range m.sinks {
		if err := sink.Publish(ctx, event); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", m.urls[i], err))
			continue
		}
		delivered++
	}

	if delivered == 0 {
		return fmt.Errorf("nenhum destino aceitou o aviso: %s", strings.Join(failures, "; "))
	}
	if len(failures) > 0 {
		// Entregue, mas com falha parcial: o texto vai para o log do drenador,
		// que o imprime sem tratar como erro fatal.
		return &PartialDelivery{Delivered: delivered, Failures: failures}
	}
	return nil
}

// PartialDelivery marca entrega feita em parte dos destinos.
//
// É tipo próprio, e não um erro comum, porque quem chama precisa distinguir
// "ninguém recebeu, tente de novo" de "alguém recebeu, mas conserte aquele
// destino". Tratar os dois igual reentregaria o aviso a quem já o tem.
type PartialDelivery struct {
	Delivered int
	Failures  []string
}

// Error descreve o que faltou entregar.
func (p *PartialDelivery) Error() string {
	return fmt.Sprintf("entregue a %d destino(s); falhou em: %s",
		p.Delivered, strings.Join(p.Failures, "; "))
}

// assertMultiSinkIsAnEventSink amarra o tipo ao porto que o drenador consome.
var _ ports.EventSink = (*MultiSink)(nil)
