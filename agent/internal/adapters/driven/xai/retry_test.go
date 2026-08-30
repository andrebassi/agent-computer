package xai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// noWait elimina a espera real do backoff. Sem isto o teste de retry dorme 6
// segundos, e teste lento é teste que alguém apaga.
func noWait(int) time.Duration { return time.Millisecond }

// countingServer devolve as respostas na ordem dada e conta quantas requisições
// recebeu. Contar é o ponto: é a única prova de que houve repetição.
func countingServer(t *testing.T, respostas ...func(w http.ResponseWriter)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(hits.Add(1)) - 1
		if i >= len(respostas) {
			i = len(respostas) - 1
		}
		respostas[i](w)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// okBody responde uma conclusão válida e mínima, com o formato que o cliente
// espera decodificar.
func okBody(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pronto"},"finish_reason":"stop"}]}`))
}

// statusBody monta uma resposta de erro com o código e o corpo dados — é o par
// que a classificação precisa para decidir a natureza da falha.
func statusBody(code int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// newTestClient monta um cliente apontado ao servidor de teste, com o backoff
// reduzido a milissegundos.
func newTestClient(t *testing.T, url string, opts ...Option) *Client {
	t.Helper()
	base := append([]Option{WithBaseURL(url), WithRetry(3, noWait)}, opts...)
	c, err := NewClient("chave-de-teste", base...)
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	return c
}

// Falha transitória é repetida, e a chamada seguinte pode dar certo.
//
// Sem isto, uma queda momentânea de rede derruba a tarefa inteira — e ela está
// segurando a trava da tela, então a tela fica reservada por um erro que teria
// passado na segunda tentativa.
func TestTransientFailureIsRetried(t *testing.T) {
	srv, hits := countingServer(t,
		statusBody(http.StatusTooManyRequests, `{"error":"slow down"}`),
		statusBody(http.StatusInternalServerError, `{"error":"oops"}`),
		okBody,
	)

	out, err := newTestClient(t, srv.URL).Complete(context.Background(),
		[]domain.Message{{Role: domain.RoleUser, Content: "oi"}}, nil)
	if err != nil {
		t.Fatalf("devia ter se recuperado: %v", err)
	}
	if out.Content != "pronto" {
		t.Fatalf("resposta errada: %q", out.Content)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("esperava 3 requisições (2 falhas + 1 boa), veio %d", got)
	}
}

// Esgotado o teto, a falha volta como ErrModelUnavailable — reconhecível por
// quem chamou, em vez de uma string de fornecedor.
func TestTransientFailureGivesUpAsPortError(t *testing.T) {
	srv, hits := countingServer(t, statusBody(http.StatusBadGateway, `{"error":"down"}`))

	_, err := newTestClient(t, srv.URL).Complete(context.Background(), nil, nil)
	if !errors.Is(err, ports.ErrModelUnavailable) {
		t.Fatalf("esperava ErrModelUnavailable, veio %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("esperava 3 tentativas, veio %d", got)
	}
}

// Erro permanente NÃO é repetido: três chamadas contra uma chave inválida dão
// exatamente o mesmo resultado e triplicam o tempo até o diagnóstico.
func TestPermanentErrorIsNotRetried(t *testing.T) {
	srv, hits := countingServer(t, statusBody(http.StatusUnauthorized, `{"error":"invalid api key"}`))

	_, err := newTestClient(t, srv.URL).Complete(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("401 devia falhar")
	}
	if errors.Is(err, ports.ErrModelUnavailable) {
		t.Fatalf("401 não é indisponibilidade: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("401 não devia ser repetido, veio %d requisições", got)
	}
}

// Janela estourada vira sentinela do PORTO, e sai sem repetir.
//
// É o que permite o serviço reagir encurtando a conversa sem precisar farejar a
// mensagem de erro do fornecedor.
func TestContextOverflowIsReportedAsPortError(t *testing.T) {
	srv, hits := countingServer(t,
		statusBody(http.StatusBadRequest, `{"error":{"message":"This model's maximum context length is 256000 tokens"}}`))

	_, err := newTestClient(t, srv.URL).Complete(context.Background(), nil, nil)
	if !errors.Is(err, ports.ErrContextTooLong) {
		t.Fatalf("esperava ErrContextTooLong, veio %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("janela estourada não devia ser repetida, veio %d", got)
	}
}

// A precedência da classificação: 429 e 5xx são transitórios MESMO quando o
// corpo menciona contexto.
//
// Um servidor sobrecarregado devolve qualquer texto, e tratar isso como janela
// estourada faria o agente descartar histórico por causa de indisponibilidade
// passageira — perde trabalho e não resolve nada.
func TestStatusPrecedenceOverBodyText(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   failureKind
	}{
		{"429 falando de contexto", http.StatusTooManyRequests, `context length exceeded`, kindTransient},
		{"503 falando de contexto", http.StatusServiceUnavailable, `maximum context`, kindTransient},
		{"400 de contexto", http.StatusBadRequest, `context window is 100`, kindContextTooLong},
		{"400 de esquema", http.StatusBadRequest, `invalid field "tolls"`, kindPermanent},
		{"413 de contexto", http.StatusRequestEntityTooLarge, `too many tokens`, kindContextTooLong},
		{"401", http.StatusUnauthorized, `bad key`, kindPermanent},
		{"404", http.StatusNotFound, `no such model`, kindPermanent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyStatus(c.status, []byte(c.body)); got != c.want {
				t.Fatalf("classificou %d/%q como %v, esperava %v", c.status, c.body, got, c.want)
			}
		})
	}
}

// Cancelamento NÃO é falha transitória.
//
// Se a pessoa desistiu, repetir gasta token contra a vontade dela. O contexto é
// consultado antes do erro porque um cancelamento embrulhado em erro de
// transporte pareceria queda de rede.
func TestCancellationIsNotTransient(t *testing.T) {
	cancelado, cancel := context.WithCancel(context.Background())
	cancel()
	if got := classifyTransport(cancelado, errors.New("connection reset")); got != kindPermanent {
		t.Fatalf("contexto cancelado devia ser permanente, veio %v", got)
	}
	if got := classifyTransport(context.Background(), context.Canceled); got != kindPermanent {
		t.Fatalf("erro de cancelamento devia ser permanente, veio %v", got)
	}
	// Erro de rede com contexto vivo continua transitório.
	if got := classifyTransport(context.Background(), errors.New("connection refused")); got != kindTransient {
		t.Fatalf("erro de rede devia ser transitório, veio %v", got)
	}
}

// O backoff acorda quando o contexto morre.
//
// `time.Sleep` puro seguraria a TRAVA DA TELA até o fim da espera mesmo depois
// de a tarefa ser cancelada — tela reservada por quem já desistiu.
func TestBackoffWakesOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	inicio := time.Now()
	err := sleepCtx(ctx, 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("esperava context.Canceled, veio %v", err)
	}
	if decorrido := time.Since(inicio); decorrido > 2*time.Second {
		t.Fatalf("dormiu %v — não acordou com o cancelamento", decorrido)
	}
}

// A espera dobra a cada tentativa, e o primeiro valor é o base.
func TestBackoffGrows(t *testing.T) {
	if backoffFor(0) != baseBackoff {
		t.Fatalf("primeira espera devia ser %v, veio %v", baseBackoff, backoffFor(0))
	}
	if backoffFor(1) != 2*baseBackoff || backoffFor(2) != 4*baseBackoff {
		t.Fatalf("a espera devia dobrar: %v, %v", backoffFor(1), backoffFor(2))
	}
}

// Cancelar DURANTE a espera devolve o motivo real, e não o erro da chamada que
// ia ser repetida — senão o diagnóstico aponta para o lugar errado.
func TestCancelDuringBackoffReturnsContextError(t *testing.T) {
	srv, _ := countingServer(t, statusBody(http.StatusInternalServerError, `{"error":"oops"}`))

	ctx, cancel := context.WithCancel(context.Background())
	client := newTestClient(t, srv.URL, WithRetry(3, func(int) time.Duration { return 5 * time.Second }))
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	_, err := client.Complete(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("esperava context.Canceled, veio %v", err)
	}
}
