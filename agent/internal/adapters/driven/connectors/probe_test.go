package connectors

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A sonda alcança um destino permitido e devolve status e corpo.
func TestProbeReachesAllowedDestination(t *testing.T) {
	allowLoopbackForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	resposta, err := Probe(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("destino permitido devia responder: %v", err)
	}
	if !strings.Contains(resposta, "HTTP 418") {
		t.Fatalf("a resposta devia trazer o status: %q", resposta)
	}
	if !strings.Contains(resposta, `{"ok":true}`) {
		t.Fatalf("a resposta devia trazer o corpo: %q", resposta)
	}
}

// A sonda recusa destino interno, e com o erro estrutural preservado.
//
// `errors.Is` importa aqui: quem chama distingue "bloqueado por política" de
// "a rede caiu", e as duas coisas se investigam em lugares diferentes.
func TestProbeRefusesInternalDestination(t *testing.T) {
	if _, err := Probe(context.Background(), "http://169.254.169.254/latest"); !errors.Is(err, ErrUnsafeBaseURL) {
		t.Fatalf("esperava ErrUnsafeBaseURL, veio %v", err)
	}
}

// Esquema fora de http/https é recusado antes de qualquer resolução.
//
// `file://` leria arquivo do disco pelo cliente HTTP — e o processo que roda
// esta sonda é o que tem o cofre aberto.
func TestProbeRefusesNonHTTPScheme(t *testing.T) {
	for _, alvo := range []string{"file:///etc/passwd", "gopher://interno:70/", "ftp://x/y"} {
		if _, err := Probe(context.Background(), alvo); !errors.Is(err, ErrUnsafeBaseURL) {
			t.Errorf("%s devia ser recusado, veio %v", alvo, err)
		}
	}
}

// Corpo grande é cortado: a sonda responde "alcancei?", não entrega conteúdo.
func TestProbeTruncatesLargeBody(t *testing.T) {
	allowLoopbackForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("A", 5000)))
	}))
	defer server.Close()

	resposta, err := Probe(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Probe falhou: %v", err)
	}
	if len(resposta) > probeBodyLimit+40 {
		t.Fatalf("corpo devia ser cortado em %d, veio %d", probeBodyLimit, len(resposta))
	}
}

// URL sintaticamente impossível vira erro, não pânico.
func TestProbeRejectsMalformedURL(t *testing.T) {
	if _, err := Probe(context.Background(), "://sem-esquema"); err == nil {
		t.Fatal("URL malformada devia falhar")
	}
}
