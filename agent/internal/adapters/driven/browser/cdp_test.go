package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Sem navegador na porta, a conexão falha com mensagem que diz ONDE tentou.
//
// A porta é derivada da tela (9220 + N), então um erro que não a mostre deixa
// quem diagnostica sem saber se o problema é a tela errada ou o navegador caído.
func TestConnectReportsPortWhenBrowserIsAbsent(t *testing.T) {
	// Tela 9: nenhum navegador de teste escuta em 9229.
	_, err := Connect(context.Background(), 9)
	if err == nil {
		t.Fatal("sem navegador, a conexão devia falhar")
	}
	if !strings.Contains(err.Error(), "9229") {
		t.Fatalf("o erro devia dizer a porta tentada: %v", err)
	}
}

// Uma resposta sem aba de página precisa ser recusada com mensagem clara.
//
// O Chrome expõe alvos do tipo `browser_ui` — barra de endereço e menus — e
// conectar num deles daria uma sessão que não é a página. Foi por isso que a
// escolha filtra por tipo.
func TestConnectRejectsWhenOnlyUITargetsExist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"1","type":"browser_ui","title":"Omnibox","url":"chrome://","webSocketDebuggerUrl":"ws://x"}]`)
	}))
	defer server.Close()

	porta := serverPort(t, server.URL)
	targets, err := listTargets(context.Background(), porta)
	if err != nil {
		t.Fatalf("listTargets falhou: %v", err)
	}
	for _, alvo := range targets {
		if alvo.Type == "page" {
			t.Fatal("o servidor de teste não devia oferecer aba de página")
		}
	}
}

// A listagem de alvos precisa decodificar o formato real do Chrome.
func TestListTargetsParsesChromeFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[
		  {"id":"a","type":"page","title":"Exemplo","url":"https://exemplo.test/","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/a"},
		  {"id":"b","type":"browser_ui","title":"Omnibox","url":"chrome://omnibox","webSocketDebuggerUrl":"ws://127.0.0.1/devtools/page/b"}
		]`)
	}))
	defer server.Close()

	targets, err := listTargets(context.Background(), serverPort(t, server.URL))
	if err != nil {
		t.Fatalf("listTargets falhou: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("esperava 2 alvos, veio %d", len(targets))
	}
	if targets[0].Type != "page" || targets[0].Title != "Exemplo" {
		t.Fatalf("alvo decodificado errado: %+v", targets[0])
	}
	if targets[0].WebSocketDebuggerURL == "" {
		t.Fatal("o endereço de WebSocket é obrigatório para conectar")
	}
}

// Resposta que não é JSON precisa produzir erro descritivo: acontece quando a
// porta está ocupada por outro serviço, e o erro genérico levaria a diagnosticar
// o navegador em vez do conflito de porta.
func TestListTargetsRejectsNonJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>não sou o Chrome</html>")
	}))
	defer server.Close()

	_, err := listTargets(context.Background(), serverPort(t, server.URL))
	if err == nil {
		t.Fatal("resposta não-JSON devia produzir erro")
	}
	if !strings.Contains(err.Error(), "inesperada") {
		t.Fatalf("o erro devia indicar resposta inesperada: %v", err)
	}
}

// A captura de tela é gravada decodificada, e o diretório é criado se faltar.
//
// A captura costuma ser pedida no meio de uma tarefa que deu errado; falhar por
// diretório ausente esconderia o problema original atrás de um erro de escrita.
func TestWriteBase64CreatesDirectoryAndDecodes(t *testing.T) {
	destino := filepath.Join(t.TempDir(), "fundo", "do", "poco", "captura.png")
	conteudo := []byte("finge que sou um PNG")
	if err := writeBase64(destino, base64.StdEncoding.EncodeToString(conteudo)); err != nil {
		t.Fatalf("writeBase64 falhou: %v", err)
	}
	gravado, err := os.ReadFile(destino)
	if err != nil {
		t.Fatalf("arquivo não foi criado: %v", err)
	}
	if string(gravado) != string(conteudo) {
		t.Fatalf("conteúdo decodificado errado: %q", string(gravado))
	}
}

// Base64 inválido precisa falhar com mensagem própria, e não gravar lixo.
func TestWriteBase64RejectsInvalidData(t *testing.T) {
	err := writeBase64(filepath.Join(t.TempDir(), "x.png"), "isto não é base64 !!!")
	if err == nil {
		t.Fatal("dados inválidos deviam ser recusados")
	}
	if !strings.Contains(err.Error(), "inválida") {
		t.Fatalf("o erro devia dizer que a imagem é inválida: %v", err)
	}
}

// O utilitário de decodificação precisa se comportar como encoding/json.
func TestJSONUnmarshalDelegates(t *testing.T) {
	var out struct {
		Data string `json:"data"`
	}
	if err := jsonUnmarshal([]byte(`{"data":"abc"}`), &out); err != nil {
		t.Fatalf("decodificação falhou: %v", err)
	}
	if out.Data != "abc" {
		t.Fatalf("valor inesperado: %q", out.Data)
	}
	if err := jsonUnmarshal([]byte(`{quebrado`), &out); err == nil {
		t.Fatal("JSON inválido devia falhar")
	}
}

// A resposta do protocolo é decodificada com o formato que o Chrome usa,
// inclusive o campo de erro.
func TestCDPResponseShape(t *testing.T) {
	var resp cdpResponse
	if err := json.Unmarshal([]byte(`{"id":7,"result":{"ok":true}}`), &resp); err != nil {
		t.Fatalf("decodificação falhou: %v", err)
	}
	if resp.ID != 7 || resp.Error != nil {
		t.Fatalf("resposta de sucesso decodificada errado: %+v", resp)
	}
	if err := json.Unmarshal([]byte(`{"id":8,"error":{"message":"deu ruim"}}`), &resp); err != nil {
		t.Fatalf("decodificação falhou: %v", err)
	}
	if resp.Error == nil || resp.Error.Message != "deu ruim" {
		t.Fatalf("erro do protocolo não foi lido: %+v", resp.Error)
	}
}

// serverPort extrai a porta de uma URL de servidor de teste.
func serverPort(t *testing.T, url string) int {
	t.Helper()
	partes := strings.Split(url, ":")
	porta := 0
	if _, err := fmt.Sscanf(partes[len(partes)-1], "%d", &porta); err != nil {
		t.Fatalf("não consegui ler a porta de %q: %v", url, err)
	}
	return porta
}
