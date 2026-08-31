package events

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// WebhookFormat escolhe como o aviso é serializado para o destino.
type WebhookFormat string

const (
	// FormatRaw envia o JSON próprio do agente. É o padrão, e o que serve a um
	// endpoint escrito para este projeto.
	FormatRaw WebhookFormat = "raw"
	// FormatNtfy envia texto puro com os metadados em cabeçalho, no contrato do
	// ntfy.sh.
	//
	// Existe porque o JSON próprio, entregue ao ntfy, chega ao celular como uma
	// linha de JSON cru — legível para uma máquina e inútil para quem é acordado
	// às três da manhã. O ntfy renderiza o CORPO como a mensagem.
	FormatNtfy WebhookFormat = "ntfy"
)

// ParseWebhookFormat lê o formato do valor de ambiente.
//
// Valor desconhecido cai em `raw` em vez de virar erro: um formato digitado
// errado não pode impedir a entrega do aviso — perder o aviso é pior que
// entregá-lo feio.
func ParseWebhookFormat(raw string) WebhookFormat {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(FormatNtfy):
		return FormatNtfy
	default:
		return FormatRaw
	}
}

// WithFormat troca a serialização do destino.
func (w *Webhook) WithFormat(format WebhookFormat) *Webhook {
	w.format = format
	return w
}

// ntfyRequest monta a requisição no contrato do ntfy.sh.
//
// Três cabeçalhos, e cada um resolve um problema concreto de quem recebe:
//
//   - `Title` põe a tela no topo da notificação, para saber ONDE agir sem abrir;
//   - `Priority` faz o pedido de take-over furar o modo silencioso do celular,
//     porque ele trava a tela até alguém agir — os outros avisos, não;
//   - `Tags` vira o emoji da notificação, que é o que se lê de relance.
//
// O corpo é o mesmo texto que aparece na tela do agente: um aviso que diverge do
// que a máquina mostra manda a pessoa procurar o que não existe.
func (w *Webhook) ntfyRequest(ctx context.Context, event domain.TaskEvent) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url,
		strings.NewReader(event.Message()))
	if err != nil {
		return nil, fmt.Errorf("montando requisição: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", ntfyTitle(event))
	req.Header.Set("Priority", ntfyPriority(event))
	req.Header.Set("Tags", ntfyTags(event))
	return req, nil
}

// ntfyTitle diz de qual tela veio, que é a primeira coisa que a pessoa precisa.
func ntfyTitle(event domain.TaskEvent) string {
	switch event.Kind {
	case domain.EventBlocked:
		return fmt.Sprintf("tela %d precisa de você", event.Screen)
	case domain.EventFailed:
		return fmt.Sprintf("tela %d falhou", event.Screen)
	default:
		return fmt.Sprintf("tela %d", event.Screen)
	}
}

// ntfyPriority reserva a prioridade alta para o que trava a tela.
//
// Marcar tudo como urgente é o mesmo que não marcar nada: quem recebe aprende a
// ignorar, e o pedido de take-over — o único que fica parado esperando gente —
// se perde no meio.
func ntfyPriority(event domain.TaskEvent) string {
	if event.Kind == domain.EventBlocked {
		return "high"
	}
	return "default"
}

// ntfyTags escolhe o emoji da notificação.
func ntfyTags(event domain.TaskEvent) string {
	switch event.Kind {
	case domain.EventBlocked:
		return "raised_hand"
	case domain.EventFailed:
		return "x"
	default:
		return "white_check_mark"
	}
}
