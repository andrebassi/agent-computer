package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// webhookTimeout é curto de propósito.
//
// Este destino é usado pelo DRENADOR, um processo separado que roda por timer.
// Um destino lento não pode fazer o drenador acumular execuções sobrepostas —
// e, se falhar, o fato continua no spool para a próxima passada.
const webhookTimeout = 15 * time.Second

// Webhook entrega o fato por HTTP.
//
// Não é o destino padrão: o padrão é o spool, que grava e retorna. Este aqui é
// chamado pelo drenador, fora do caminho da tarefa — é o que impede um serviço
// remoto de segurar a trava da tela enquanto responde.
type Webhook struct {
	url    string
	client *http.Client
}

// NewWebhook monta o destino. URL vazia é erro na construção, e não no primeiro
// envio: descobrir isso no meio de um aviso urgente é tarde demais.
func NewWebhook(url string) (*Webhook, error) {
	if url == "" {
		return nil, fmt.Errorf("URL do webhook vazia")
	}
	return &Webhook{url: url, client: &http.Client{Timeout: webhookTimeout}}, nil
}

// WithClient troca o cliente HTTP, para o teste controlar o transporte.
func (w *Webhook) WithClient(client *http.Client) *Webhook {
	w.client = client
	return w
}

// webhookPayload é o corpo enviado.
//
// Leva a mensagem já renderizada além dos campos crus: quem recebe costuma ser um
// bot de chat que só precisa repassar texto, e obrigá-lo a reimplementar a
// formatação faria o aviso do chat divergir do que a tela mostra.
type webhookPayload struct {
	TaskID  string    `json:"task_id"`
	Screen  int       `json:"screen"`
	Kind    string    `json:"kind"`
	Reason  string    `json:"reason,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Summary string    `json:"summary,omitempty"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Publish envia o fato e devolve erro quando a entrega não se confirma.
//
// Qualquer resposta fora da faixa 2xx conta como falha, para o drenador manter o
// fato na fila e tentar de novo. Aceitar 4xx como sucesso perderia o aviso em
// silêncio — que é exatamente o que este mecanismo existe para impedir.
func (w *Webhook) Publish(ctx context.Context, event domain.TaskEvent) error {
	body, err := json.Marshal(webhookPayload{
		TaskID:  event.TaskID,
		Screen:  event.Screen,
		Kind:    string(event.Kind),
		Reason:  string(event.Reason),
		Detail:  event.Detail,
		Summary: event.Summary,
		Message: event.Message(),
		At:      event.At,
	})
	if err != nil {
		return fmt.Errorf("serializando aviso: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("montando requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("entregando aviso: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("o destino recusou o aviso: HTTP %d", resp.StatusCode)
	}
	return nil
}
