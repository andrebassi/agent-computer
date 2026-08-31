package events

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// blockedEvent monta um pedido de take-over, que é o aviso que trava a tela.
func blockedEvent() domain.TaskEvent {
	return domain.TaskEvent{
		TaskID: "task-1", Screen: 3,
		Kind:   domain.EventBlocked,
		Reason: domain.BlockPassword,
		Detail: "a página pede usuário e senha",
		At:     time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC),
	}
}

// No formato ntfy o corpo é o TEXTO, e não JSON.
//
// É a razão de o formato existir: entregue como JSON, o aviso chega ao celular
// como uma linha de JSON cru — legível para uma máquina e inútil para quem é
// acordado de madrugada por ele.
func TestNtfyFormatSendsPlainText(t *testing.T) {
	var body, title, priority, tags, contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		title = r.Header.Get("Title")
		priority = r.Header.Get("Priority")
		tags = r.Header.Get("Tags")
		contentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hook, err := NewWebhook(server.URL)
	if err != nil {
		t.Fatalf("montando o destino: %v", err)
	}
	event := blockedEvent()
	if err := hook.WithFormat(FormatNtfy).Publish(context.Background(), event); err != nil {
		t.Fatalf("publicando: %v", err)
	}

	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("o corpo devia ser texto, veio JSON: %s", body)
	}
	if body != event.Message() {
		t.Errorf("o corpo devia ser o MESMO texto da tela.\n  tela:  %q\n  envio: %q",
			event.Message(), body)
	}
	if !strings.Contains(title, "3") {
		t.Errorf("o título devia dizer a tela: %q", title)
	}
	// Prioridade alta só no que trava a tela — ver a próxima verificação.
	if priority != "high" {
		t.Errorf("take-over devia ir com prioridade alta, veio %q", priority)
	}
	if tags == "" {
		t.Error("faltou a etiqueta que vira emoji da notificação")
	}
	if !strings.HasPrefix(contentType, "text/plain") {
		t.Errorf("Content-Type devia ser text/plain, veio %q", contentType)
	}
}

// Aviso que NÃO trava a tela vai com prioridade normal.
//
// O outro sentido do teste acima: marcar tudo como urgente ensina quem recebe a
// ignorar, e aí o pedido de take-over — o único que fica parado esperando gente
// — se perde no meio dos demais.
func TestNtfyReservesHighPriorityForTakeOver(t *testing.T) {
	var priority string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		priority = r.Header.Get("Priority")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hook, _ := NewWebhook(server.URL)
	event := blockedEvent()
	event.Kind = domain.EventFinished
	event.Reason = ""
	if err := hook.WithFormat(FormatNtfy).Publish(context.Background(), event); err != nil {
		t.Fatalf("publicando: %v", err)
	}
	if priority == "high" {
		t.Error("tarefa concluída não devia furar o modo silencioso")
	}
}

// Sem escolher formato, o destino continua enviando o JSON de sempre.
//
// Compatibilidade importa aqui porque o `raw` é o contrato de qualquer endpoint
// já escrito para este projeto: mudar o padrão quebraria em silêncio.
func TestDefaultFormatStaysJSON(t *testing.T) {
	var body, contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		contentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hook, _ := NewWebhook(server.URL)
	if err := hook.Publish(context.Background(), blockedEvent()); err != nil {
		t.Fatalf("publicando: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("o padrão devia continuar JSON: %s", body)
	}
	if !strings.Contains(body, `"task_id"`) {
		t.Errorf("o JSON devia trazer os campos crus: %s", body)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type errado: %q", contentType)
	}
}

// Formato desconhecido cai em `raw` em vez de virar erro.
//
// Um valor digitado errado na configuração não pode impedir a entrega: perder o
// aviso é pior que entregá-lo no formato feio.
func TestUnknownFormatFallsBackToRaw(t *testing.T) {
	for _, raw := range []string{"", "NTFY_2", "slack", "  "} {
		if got := ParseWebhookFormat(raw); got != FormatRaw {
			t.Errorf("%q devia cair em raw, veio %q", raw, got)
		}
	}
	// E o sentido oposto: o nome certo é reconhecido, com ou sem maiúscula e
	// espaço em volta — configuração é escrita à mão.
	for _, raw := range []string{"ntfy", "NTFY", " ntfy "} {
		if got := ParseWebhookFormat(raw); got != FormatNtfy {
			t.Errorf("%q devia ser reconhecido como ntfy, veio %q", raw, got)
		}
	}
}
