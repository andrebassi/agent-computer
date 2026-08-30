package events

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// O webhook entrega o fato com a mensagem JÁ RENDERIZADA.
//
// Quem recebe costuma ser um bot de chat que só repassa texto; obrigá-lo a
// reimplementar a formatação faria o aviso do chat divergir do da tela.
func TestWebhookSendsRenderedMessage(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hook, err := NewWebhook(server.URL)
	if err != nil {
		t.Fatalf("NewWebhook falhou: %v", err)
	}
	event := domain.TaskEvent{TaskID: "t1", Screen: 2, Kind: domain.EventBlocked, Reason: domain.BlockPassword}
	if err := hook.Publish(context.Background(), event); err != nil {
		t.Fatalf("Publish falhou: %v", err)
	}
	if !strings.Contains(received, "PRECISA DE VOCÊ") {
		t.Fatalf("a mensagem renderizada devia ir junto: %s", received)
	}
	if !strings.Contains(received, `"task_id":"t1"`) {
		t.Fatalf("os campos crus também: %s", received)
	}
}

// Qualquer resposta fora de 2xx conta como falha.
//
// Aceitar 4xx como sucesso perderia o aviso em silêncio — que é exatamente o
// que este mecanismo existe para impedir.
func TestWebhookTreatsNon2xxAsFailure(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
		status := code
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		hook, err := NewWebhook(server.URL)
		if err != nil {
			t.Fatalf("NewWebhook falhou: %v", err)
		}
		if err := hook.Publish(context.Background(), domain.TaskEvent{}); err == nil {
			t.Fatalf("HTTP %d devia ser falha", status)
		}
		server.Close()
	}
}

// URL vazia falha na construção, e não no primeiro envio — descobrir isso no
// meio de um aviso urgente é tarde demais.
func TestNewWebhookRejectsEmptyURL(t *testing.T) {
	if _, err := NewWebhook(""); err == nil {
		t.Fatal("URL vazia devia falhar")
	}
}

// Destino inalcançável devolve erro, para o fato ficar na fila.
func TestWebhookReportsUnreachableDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	hook, err := NewWebhook(url)
	if err != nil {
		t.Fatalf("NewWebhook falhou: %v", err)
	}
	if err := hook.Publish(context.Background(), domain.TaskEvent{}); err == nil {
		t.Fatal("destino fora do ar devia devolver erro")
	}
}

// WithClient troca o transporte e devolve o próprio webhook, para encadear.
func TestWebhookWithClient(t *testing.T) {
	hook, err := NewWebhook("http://exemplo.invalido")
	if err != nil {
		t.Fatalf("NewWebhook falhou: %v", err)
	}
	if hook.WithClient(&http.Client{}) != hook {
		t.Fatal("WithClient devia devolver o próprio webhook")
	}
}
