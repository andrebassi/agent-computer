package xai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// Chave vazia é recusada na construção: descobrir isso só na primeira chamada
// desperdiçaria uma tarefa inteira.
func TestNewClientRejectsEmptyKey(t *testing.T) {
	if _, err := NewClient(""); err == nil {
		t.Fatal("chave vazia devia ser recusada")
	}
}

// O caminho normal: a resposta da API vira uma Completion com as chamadas de
// ferramenta e o uso de tokens, que alimenta o controle de custo.
func TestCompleteParsesToolCallsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer chave-de-teste" {
			t.Errorf("cabeçalho de autorização errado: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
          "choices":[{"message":{"role":"assistant","content":"vou rodar",
            "tool_calls":[{"id":"c1","type":"function",
              "function":{"name":"shell","arguments":"{\"command\":\"pwd\"}"}}]},
            "finish_reason":"tool_calls"}],
          "usage":{"prompt_tokens":723,"completion_tokens":10}}`)
	}))
	defer server.Close()

	client, err := NewClient("chave-de-teste", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	got, err := client.Complete(context.Background(),
		[]domain.Message{{Role: domain.RoleUser, Content: "rode pwd"}},
		[]ports.ToolSpec{{Name: "shell", Description: "roda comando", Schema: `{"type":"object"}`}})
	if err != nil {
		t.Fatalf("Complete falhou: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "shell" {
		t.Fatalf("chamada de ferramenta não foi lida: %+v", got.ToolCalls)
	}
	if got.ToolCalls[0].Arguments != `{"command":"pwd"}` {
		t.Fatalf("argumentos vieram diferentes: %q", got.ToolCalls[0].Arguments)
	}
	if got.PromptTokens != 723 || got.CompletionTokens != 10 {
		t.Fatalf("uso de tokens não foi lido: %d/%d", got.PromptTokens, got.CompletionTokens)
	}
	if got.StopReason != "tool_calls" {
		t.Fatalf("motivo de parada errado: %q", got.StopReason)
	}
}

// O corpo enviado precisa levar histórico e ferramentas no formato que a API
// espera; um campo errado só apareceria como recusa remota, difícil de ler.
func TestCompleteSendsToolsAndHistory(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("corpo inválido: %v", err)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	client, err := NewClient("k", WithBaseURL(server.URL), WithModel("grok-teste"))
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	messages := []domain.Message{
		{Role: domain.RoleSystem, Content: "instruções"},
		{Role: domain.RoleAssistant, Content: "chamando", ToolCalls: []domain.ToolCall{{ID: "c1", Name: "shell", Arguments: "{}"}}},
		{Role: domain.RoleTool, Content: "saída", ToolCallID: "c1"},
	}
	if _, err := client.Complete(context.Background(), messages,
		[]ports.ToolSpec{{Name: "shell", Schema: `{"type":"object"}`}}); err != nil {
		t.Fatalf("Complete falhou: %v", err)
	}

	if sent["model"] != "grok-teste" {
		t.Fatalf("modelo não foi aplicado: %v", sent["model"])
	}
	if sent["tool_choice"] != "auto" {
		t.Fatalf("tool_choice devia ser auto: %v", sent["tool_choice"])
	}
	list, ok := sent["messages"].([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("histórico incompleto: %v", sent["messages"])
	}
	last, ok := list[2].(map[string]any)
	if !ok || last["tool_call_id"] != "c1" {
		t.Fatal("o id da chamada precisa ir junto do resultado, senão a API recusa o turno")
	}
}

// Sem ferramentas, o campo correspondente não deve ir no corpo: algumas APIs
// recusam lista vazia.
func TestCompleteOmitsToolsWhenThereAreNone(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("corpo inválido: %v", err)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	client, err := NewClient("k", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	if _, err := client.Complete(context.Background(), []domain.Message{{Role: domain.RoleUser, Content: "oi"}}, nil); err != nil {
		t.Fatalf("Complete falhou: %v", err)
	}
	if _, found := sent["tools"]; found {
		t.Fatal("o campo tools não devia ser enviado quando não há ferramentas")
	}
}

// Erro HTTP precisa virar erro claro, com o código, e sem despejar o corpo
// inteiro — que pode conter o histórico e, com ele, dado sensível.
func TestCompleteReportsHTTPErrorWithoutDumpingEverything(t *testing.T) {
	hugeBody := strings.Repeat("conteudo-do-historico ", 500)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, hugeBody)
	}))
	defer server.Close()

	client, err := NewClient("k", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	_, err = client.Complete(context.Background(), []domain.Message{{Role: domain.RoleUser, Content: "oi"}}, nil)
	if err == nil {
		t.Fatal("status 401 devia produzir erro")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("o erro devia dizer o código: %v", err)
	}
	if len(err.Error()) > 500 {
		t.Fatalf("o erro não devia despejar o corpo inteiro (%d caracteres)", len(err.Error()))
	}
}

// Resposta sem choices é malformada e não pode virar uma Completion vazia, que
// o laço interpretaria como tarefa concluída.
func TestCompleteRejectsResponseWithoutChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer server.Close()

	client, err := NewClient("k", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	if _, err := client.Complete(context.Background(), nil, nil); err == nil {
		t.Fatal("resposta sem choices devia produzir erro")
	}
}

// JSON inválido na resposta precisa produzir erro, e não silêncio.
func TestCompleteRejectsMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{resposta quebrada`)
	}))
	defer server.Close()

	client, err := NewClient("k", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	if _, err := client.Complete(context.Background(), nil, nil); err == nil {
		t.Fatal("JSON inválido devia produzir erro")
	}
}

// Servidor fora do ar vira erro de rede tratado, não pânico.
func TestCompleteHandlesNetworkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := server.URL
	server.Close()

	// WithRetry entra aqui porque servidor fora do ar é falha TRANSITÓRIA, e
	// desde que o retry existe esta chamada repete três vezes. Com o backoff de
	// produção o teste passou de instantâneo a 6 segundos — e teste lento é
	// teste que alguém apaga.
	client, err := NewClient("k", WithBaseURL(url), WithRetry(3, noWait))
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	if _, err := client.Complete(context.Background(), nil, nil); err == nil {
		t.Fatal("servidor fora do ar devia produzir erro")
	}
}

// WithHTTPClient existe para o teste controlar o transporte; sem cobertura, a
// opção poderia quebrar sem ninguém notar.
func TestWithHTTPClientIsApplied(t *testing.T) {
	custom := &http.Client{}
	client, err := NewClient("k", WithHTTPClient(custom))
	if err != nil {
		t.Fatalf("NewClient falhou: %v", err)
	}
	if client.http != custom {
		t.Fatal("o cliente HTTP fornecido devia ser usado")
	}
}

// O corte de texto longo não pode partir um caractere multibyte ao meio, o que
// produziria bytes inválidos no log.
func TestTruncateKeepsMultibyteCharactersIntact(t *testing.T) {
	long := strings.Repeat("ç", 400)
	got := truncate(long, 100)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("texto longo devia ser cortado: %q", got[:20])
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("o corte partiu um caractere multibyte")
		}
	}
	if short := truncate("curto", 100); short != "curto" {
		t.Fatalf("texto curto foi alterado: %q", short)
	}
}
