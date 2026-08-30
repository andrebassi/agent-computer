package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// testToken tem o tamanho mínimo exigido.
const testToken = "0123456789abcdef0123456789abcdef"

// newServer monta um servidor com duplos e devolve também o armazenamento, para
// o teste inspecionar o que ficou gravado.
func newServer(t *testing.T, runner *fakeRunner) (http.Handler, *fakeStore, *Supervisor) {
	t.Helper()
	store := newFakeStore()
	sup, _ := newSupervisor(t, runner, store, &fakeLock{})
	life := service.NewLifecycle(store, &fakeScreen{}, time.Now)
	srv, err := NewServer(sup, life, store, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewServer falhou: %v", err)
	}
	return srv.Handler(), store, sup
}

// do executa uma requisição autenticada e devolve a resposta.
func do(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// decode lê o corpo JSON da resposta.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("corpo não é JSON: %v — %s", err, rec.Body.String())
	}
	return out
}

// Criar devolve 201 com o identificador e o cabeçalho de localização.
func TestCreateTaskReturns201(t *testing.T) {
	runner := &fakeRunner{release: make(chan struct{})}
	handler, _, _ := newServer(t, runner)

	rec := do(t, handler, http.MethodPost, "/tasks", `{"prompt":"conte os núcleos","screen":2}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("esperava 201, veio %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["id"] == "" || body["screen"] != float64(2) {
		t.Fatalf("corpo inesperado: %v", body)
	}
	if loc := rec.Header().Get("Location"); loc == "" {
		t.Fatal("devia haver cabeçalho Location apontando para a tarefa")
	}
	close(runner.release)
}

// Sem token, ou com token errado, é 401 — e o formato do cabeçalho não importa.
func TestAuthenticationRejectsBadTokens(t *testing.T) {
	handler, _, _ := newServer(t, &fakeRunner{})
	cases := []struct {
		name      string
		header string
	}{
		{"sem cabeçalho", ""},
		{"token errado", "Bearer token-errado-mas-do-tamanho-certo!!"},
		{"esquema errado", "Basic " + testToken},
		{"token vazio", "Bearer "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(`{"prompt":"x"}`))
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("esperava 401, veio %d", rec.Code)
			}
		})
	}
}

// Saúde responde SEM token.
//
// Autenticá-la obrigaria o supervisor de processo a carregar o segredo só para
// provar que a porta responde.
func TestHealthNeedsNoToken(t *testing.T) {
	handler, _, _ := newServer(t, &fakeRunner{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", rec.Code)
	}
	if decode(t, rec)["status"] != "ok" {
		t.Fatalf("corpo inesperado: %s", rec.Body.String())
	}
}

// Tela ocupada devolve 409 dizendo O QUE FAZER.
//
// Sem o id e a dica, quem chamou precisa adivinhar se retoma ou abandona.
func TestBusyScreenReturns409WithHint(t *testing.T) {
	runner := &fakeRunner{release: make(chan struct{})}
	handler, _, _ := newServer(t, runner)

	firstResponse := do(t, handler, http.MethodPost, "/tasks", `{"prompt":"primeira","screen":1}`)
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("preparação falhou: %d", firstResponse.Code)
	}
	secondResponse := do(t, handler, http.MethodPost, "/tasks", `{"prompt":"segunda","screen":1}`)
	if secondResponse.Code != http.StatusConflict {
		t.Fatalf("esperava 409, veio %d", secondResponse.Code)
	}
	body := decode(t, secondResponse)
	if body["task_id"] != decode(t, firstResponse)["id"] {
		t.Fatalf("devia nomear a tarefa que segura a tela: %v", body)
	}
	if hint, _ := body["hint"].(string); !strings.Contains(hint, "resume") || !strings.Contains(hint, "abandon") {
		t.Fatalf("a dica devia dizer o que fazer: %v", body["hint"])
	}
	close(runner.release)
}

// Pedido malformado é 400, não 500 — a culpa é de quem chamou.
func TestBadRequestsReturn400(t *testing.T) {
	handler, _, _ := newServer(t, &fakeRunner{})
	cases := map[string]string{
		"json quebrado": `{quebrado`,
		"prompt vazio":  `{"prompt":"","screen":1}`,
		"tela inválida": `{"prompt":"x","screen":99}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(t, handler, http.MethodPost, "/tasks", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("esperava 400, veio %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Corpo grande demais é 413, e é recusado ANTES de virar memória do processo.
func TestOversizedBodyReturns413(t *testing.T) {
	handler, _, _ := newServer(t, &fakeRunner{})
	oversized := `{"prompt":"` + strings.Repeat("x", maxBodyBytes+1024) + `"}`
	rec := do(t, handler, http.MethodPost, "/tasks", oversized)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("esperava 413, veio %d", rec.Code)
	}
}

// Consultar tarefa inexistente é 404.
func TestGetUnknownTaskReturns404(t *testing.T) {
	handler, _, _ := newServer(t, &fakeRunner{})
	rec := do(t, handler, http.MethodGet, "/tasks/não-existe", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("esperava 404, veio %d", rec.Code)
	}
}

// A consulta devolve a RESPOSTA da tarefa, não só o estado.
//
// Um cliente que só visse o estado saberia que a tarefa terminou, e nunca o que
// ela respondeu — que é o motivo de tê-la disparado.
func TestGetTaskIncludesTheAnswer(t *testing.T) {
	handler, store, _ := newServer(t, &fakeRunner{})

	task, err := domain.NewTask("t1", 1, "faça algo", time.Now())
	if err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	_ = task.Start(time.Now())
	_ = task.Finish(time.Now())
	store.tasks["t1"] = task

	conv := domain.NewConversation("t1", "instruções")
	conv.AddUser("faça algo")
	conv.AddAssistant("encontrei 4 núcleos", nil)
	if err := store.SaveConversation(context.Background(), conv); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	rec := do(t, handler, http.MethodGet, "/tasks/t1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", rec.Code)
	}
	if decode(t, rec)["answer"] != "encontrei 4 núcleos" {
		t.Fatalf("a resposta devia vir junto: %s", rec.Body.String())
	}
}

// Retomar tarefa que não está bloqueada é 409; inexistente é 404.
func TestResumeStatusCodes(t *testing.T) {
	handler, store, _ := newServer(t, &fakeRunner{})

	if rec := do(t, handler, http.MethodPost, "/tasks/sumida/resume", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("inexistente devia ser 404, veio %d", rec.Code)
	}

	runningTask, err := domain.NewTask("t2", 1, "faça algo", time.Now())
	if err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	_ = runningTask.Start(time.Now())
	store.tasks["t2"] = runningTask
	if rec := do(t, handler, http.MethodPost, "/tasks/t2/resume", `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("não bloqueada devia ser 409, veio %d", rec.Code)
	}
}

// Abandonar libera a tela e devolve 200 com a tarefa encerrada.
func TestAbandonFreesTheScreen(t *testing.T) {
	handler, store, _ := newServer(t, &fakeRunner{})
	blockedTaskInStore(t, store, "t1", 1)

	rec := do(t, handler, http.MethodPost, "/tasks/t1/abandon", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d: %s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["state"] != string(domain.StateFailed) {
		t.Fatalf("a tarefa devia constar encerrada: %s", rec.Body.String())
	}
	// E a tela precisa aceitar trabalho novo.
	if novo := do(t, handler, http.MethodPost, "/tasks", `{"prompt":"nova","screen":1}`); novo.Code != http.StatusCreated {
		t.Fatalf("a tela devia estar livre, veio %d: %s", novo.Code, novo.Body.String())
	}
}

// Servidor sem token não sobe.
//
// Uma porta que abre porque o segredo sumiu é o pior desfecho: tudo funciona e
// ninguém percebe que está aberta.
func TestNewServerRefusesEmptyToken(t *testing.T) {
	store := newFakeStore()
	sup, _ := newSupervisor(t, &fakeRunner{}, store, &fakeLock{})
	life := service.NewLifecycle(store, &fakeScreen{}, time.Now)
	if _, err := NewServer(sup, life, store, "", quietLogger()); err == nil {
		t.Fatal("token vazio devia impedir a construção")
	}
}
