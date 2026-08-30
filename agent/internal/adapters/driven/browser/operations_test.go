package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// fakeChrome simula o protocolo de depuração: recebe comandos e responde com o
// que o roteiro mandar.
//
// Testar contra o Chrome de verdade tornaria a suíte dependente de ter um
// navegador instalado e de a internet estar de pé — e o que se quer verificar
// aqui é o NOSSO lado do protocolo, não o dele.
type fakeChrome struct {
	server *httptest.Server
	// responder devolve o resultado de cada comando, pelo nome do método.
	responder func(method string, params map[string]any) (any, error)
	// received guarda os comandos na ordem, para as asserções.
	received []string
}

// newFakeChrome sobe o servidor que serve tanto a listagem de alvos quanto o
// WebSocket do protocolo.
func newFakeChrome(t *testing.T, responder func(string, map[string]any) (any, error)) *fakeChrome {
	t.Helper()
	fake := &fakeChrome{responder: responder}
	mux := http.NewServeMux()

	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		ws := "ws://" + r.Host + "/devtools/page/teste"
		_, _ = w.Write([]byte(`[{"id":"teste","type":"page","title":"Fake","url":"about:blank","webSocketDebuggerUrl":"` + ws + `"}]`))
	})

	mux.HandleFunc("/devtools/page/teste", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}
			fake.received = append(fake.received, req.Method)

			result, cdpErr := fake.responder(req.Method, req.Params)
			resposta := map[string]any{"id": req.ID}
			if cdpErr != nil {
				resposta["error"] = map[string]any{"message": cdpErr.Error()}
			} else {
				resposta["result"] = result
			}
			body, _ := json.Marshal(resposta)
			if err := conn.Write(r.Context(), websocket.MessageText, body); err != nil {
				return
			}
		}
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

// connect devolve um cliente ligado ao Chrome falso, com a pausa zerada.
func (f *fakeChrome) connect(t *testing.T) *Client {
	t.Helper()
	targets, err := listTargets(context.Background(), serverPort(t, f.server.URL))
	if err != nil {
		t.Fatalf("listTargets falhou: %v", err)
	}
	conn, _, err := websocket.Dial(context.Background(), targets[0].WebSocketDebuggerURL, nil)
	if err != nil {
		t.Fatalf("conexão falhou: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	// settle zerado: a pausa existe para a página assentar, e aqui não há página.
	return &Client{conn: conn, timeout: commandTimeout, settle: 0}
}

// evaluated monta a resposta que o Chrome dá a Runtime.evaluate.
func evaluated(value any) map[string]any {
	return map[string]any{"result": map[string]any{"value": value}}
}

// Navegar envia o comando certo e devolve título e endereço finais — é o que
// permite ao agente perceber um redirecionamento para login.
func TestNavigateSendsCommandAndDescribesResult(t *testing.T) {
	fake := newFakeChrome(t, func(method string, params map[string]any) (any, error) {
		switch method {
		case "Page.navigate":
			if params["url"] != "https://exemplo.test/pagina" {
				t.Errorf("URL enviada errada: %v", params["url"])
			}
			return map[string]any{}, nil
		case "Runtime.evaluate":
			return evaluated("Título | https://exemplo.test/login"), nil
		}
		return map[string]any{}, nil
	})

	got, err := fake.connect(t).Navigate(context.Background(), "https://exemplo.test/pagina")
	if err != nil {
		t.Fatalf("Navigate falhou: %v", err)
	}
	if !strings.Contains(got, "login") {
		t.Fatalf("o endereço FINAL devia voltar, para o agente ver o redirecionamento: %q", got)
	}
}

// URL sem esquema ganha https, senão o Chrome trata como caminho relativo e a
// navegação vai para lugar nenhum, sem erro.
func TestNavigateAddsSchemeWhenMissing(t *testing.T) {
	var enviada string
	fake := newFakeChrome(t, func(method string, params map[string]any) (any, error) {
		if method == "Page.navigate" {
			enviada, _ = params["url"].(string)
		}
		return evaluated("t | u"), nil
	})
	if _, err := fake.connect(t).Navigate(context.Background(), "exemplo.test"); err != nil {
		t.Fatalf("Navigate falhou: %v", err)
	}
	if enviada != "https://exemplo.test" {
		t.Fatalf("devia acrescentar o esquema: %q", enviada)
	}
}

// Texto longo é cortado, porque ele entra no histórico e é cobrado a cada
// iteração seguinte.
func TestReadTextTruncatesLongPages(t *testing.T) {
	longo := strings.Repeat("a", maxTextBytes+500)
	fake := newFakeChrome(t, func(string, map[string]any) (any, error) {
		return evaluated(longo), nil
	})
	got, err := fake.connect(t).ReadText(context.Background())
	if err != nil {
		t.Fatalf("ReadText falhou: %v", err)
	}
	if len(got) >= len(longo) {
		t.Fatal("página longa devia ser cortada")
	}
	if !strings.Contains(got, "truncada") {
		t.Fatalf("devia avisar que cortou: %q", got[len(got)-60:])
	}
}

// Clicar num rótulo que não existe precisa dizer COMO descobrir os que existem.
//
// Sem essa dica, o modelo tenta variações do mesmo rótulo até bater o teto de
// iterações — comportamento observado em agentes que só recebem "não encontrado".
func TestClickSuggestsListingLinksWhenLabelIsMissing(t *testing.T) {
	fake := newFakeChrome(t, func(string, map[string]any) (any, error) {
		return evaluated("NAO_ENCONTRADO"), nil
	})
	_, err := fake.connect(t).Click(context.Background(), "Botão Fantasma")
	if err == nil {
		t.Fatal("rótulo inexistente devia falhar")
	}
	if !strings.Contains(err.Error(), "browser_links") {
		t.Fatalf("o erro devia dizer como listar o que existe: %v", err)
	}
}

// Clique bem-sucedido informa onde a página foi parar.
func TestClickReportsWhereItLanded(t *testing.T) {
	chamadas := 0
	fake := newFakeChrome(t, func(method string, _ map[string]any) (any, error) {
		if method != "Runtime.evaluate" {
			return map[string]any{}, nil
		}
		chamadas++
		if chamadas == 1 {
			return evaluated("cliquei em: Saiba mais"), nil
		}
		return evaluated("Destino | https://exemplo.test/destino"), nil
	})
	got, err := fake.connect(t).Click(context.Background(), "Saiba mais")
	if err != nil {
		t.Fatalf("Click falhou: %v", err)
	}
	if !strings.Contains(got, "agora em") || !strings.Contains(got, "destino") {
		t.Fatalf("devia informar o destino: %q", got)
	}
}

// Campo inexistente precisa lembrar que senha é recusada de propósito, senão o
// modelo conclui que a ferramenta está quebrada.
func TestFillMentionsPasswordExclusionOnFailure(t *testing.T) {
	fake := newFakeChrome(t, func(string, map[string]any) (any, error) {
		return evaluated("NAO_ENCONTRADO"), nil
	})
	_, err := fake.connect(t).Fill(context.Background(), "senha", "algo")
	if err == nil {
		t.Fatal("campo inexistente devia falhar")
	}
	if !strings.Contains(err.Error(), "senha são ignorados") {
		t.Fatalf("o erro devia explicar a exclusão deliberada: %v", err)
	}
}

// Preenchimento bem-sucedido devolve qual campo foi tocado.
func TestFillReportsWhichFieldWasTouched(t *testing.T) {
	fake := newFakeChrome(t, func(string, map[string]any) (any, error) {
		return evaluated("preenchi login"), nil
	})
	got, err := fake.connect(t).Fill(context.Background(), "login", "andre")
	if err != nil {
		t.Fatalf("Fill falhou: %v", err)
	}
	if !strings.Contains(got, "login") {
		t.Fatalf("devia dizer qual campo: %q", got)
	}
}

// Os links voltam com o texto de cada um, que é o que o modelo usa para decidir.
func TestListLinksReturnsLabels(t *testing.T) {
	fake := newFakeChrome(t, func(string, map[string]any) (any, error) {
		return evaluated("Documentação  ->  https://exemplo.test/docs"), nil
	})
	got, err := fake.connect(t).ListLinks(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListLinks falhou: %v", err)
	}
	if !strings.Contains(got, "Documentação") {
		t.Fatalf("os rótulos deviam voltar: %q", got)
	}
}

// A captura é decodificada e gravada no caminho pedido.
func TestScreenshotWritesFile(t *testing.T) {
	conteudo := []byte("PNG de mentira")
	fake := newFakeChrome(t, func(method string, _ map[string]any) (any, error) {
		if method == "Page.captureScreenshot" {
			return map[string]any{"data": base64.StdEncoding.EncodeToString(conteudo)}, nil
		}
		return map[string]any{}, nil
	})
	destino := filepath.Join(t.TempDir(), "captura.png")
	got, err := fake.connect(t).Screenshot(context.Background(), destino)
	if err != nil {
		t.Fatalf("Screenshot falhou: %v", err)
	}
	if got != destino {
		t.Fatalf("caminho devolvido errado: %q", got)
	}
}

// Erro do protocolo precisa chegar a quem chamou com o método que falhou.
func TestProtocolErrorIsPropagated(t *testing.T) {
	fake := newFakeChrome(t, func(string, map[string]any) (any, error) {
		return nil, errFake("alvo não encontrado")
	})
	_, err := fake.connect(t).ReadText(context.Background())
	if err == nil {
		t.Fatal("erro do protocolo devia propagar")
	}
	if !strings.Contains(err.Error(), "Runtime.evaluate") {
		t.Fatalf("o erro devia nomear o método que falhou: %v", err)
	}
}

// Exceção na página é distinguida de erro de protocolo: uma é culpa do script,
// a outra do navegador, e confundi-las manda o diagnóstico para o lado errado.
func TestPageExceptionIsReportedSeparately(t *testing.T) {
	fake := newFakeChrome(t, func(string, map[string]any) (any, error) {
		return map[string]any{
			"result":           map[string]any{},
			"exceptionDetails": map[string]any{"text": "TypeError: x is not a function"},
		}, nil
	})
	_, err := fake.connect(t).ReadText(context.Background())
	if err == nil {
		t.Fatal("exceção na página devia falhar")
	}
	if !strings.Contains(err.Error(), "a página devolveu erro") {
		t.Fatalf("devia distinguir exceção de página: %v", err)
	}
}

// Connect completo: descobre o alvo, abre o WebSocket e fecha limpo.
//
// Escuta na porta 9229 — a que a tela 9 usaria — para exercitar o cálculo
// `9220 + tela`, que é onde um erro de aritmética passaria despercebido.
func TestConnectAndCloseAgainstFakeChrome(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		ws := "ws://" + r.Host + "/devtools/page/x"
		_, _ = w.Write([]byte(`[
		  {"id":"ui","type":"browser_ui","title":"Omnibox","url":"chrome://","webSocketDebuggerUrl":"ws://ignorar"},
		  {"id":"x","type":"page","title":"Página","url":"about:blank","webSocketDebuggerUrl":"` + ws + `"}
		]`))
	})
	mux.HandleFunc("/devtools/page/x", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		// Ler em laço é o que faz o servidor responder ao handshake de
		// fechamento. Bloquear no contexto deixaria o Close do cliente esperando
		// resposta até estourar o prazo — e o teste acusaria falha num Close que
		// está correto.
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	})

	listener, err := net.Listen("tcp", "127.0.0.1:9229")
	if err != nil {
		t.Skipf("porta 9229 ocupada nesta máquina: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: mux}}
	server.Start()
	defer server.Close()

	client, err := Connect(context.Background(), 9)
	if err != nil {
		t.Fatalf("Connect falhou: %v", err)
	}
	// A escolha precisa ter pulado o alvo `browser_ui`: conectar nele daria uma
	// sessão que não é a página, e todos os comandos seguintes falhariam de
	// formas confusas.
	if client.port != 9229 {
		t.Fatalf("porta calculada errada: %d", client.port)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close falhou: %v", err)
	}
}

// Quando só há alvos de interface, a mensagem precisa dizer que falta ABA, e não
// que o navegador está fora do ar — são causas diferentes.
func TestConnectRejectsWhenNoPageTarget(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"ui","type":"browser_ui","title":"x","url":"chrome://","webSocketDebuggerUrl":"ws://x"}]`))
	})
	listener, err := net.Listen("tcp", "127.0.0.1:9228")
	if err != nil {
		t.Skipf("porta 9228 ocupada: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: mux}}
	server.Start()
	defer server.Close()

	_, err = Connect(context.Background(), 8)
	if err == nil {
		t.Fatal("sem aba de página, devia falhar")
	}
	if !strings.Contains(err.Error(), "aba de página") {
		t.Fatalf("a mensagem devia distinguir falta de aba: %v", err)
	}
}

// Falha na navegação não pode ser mascarada como sucesso vazio.
func TestNavigatePropagatesCommandFailure(t *testing.T) {
	fake := newFakeChrome(t, func(method string, _ map[string]any) (any, error) {
		if method == "Page.navigate" {
			return nil, errFake("net::ERR_NAME_NOT_RESOLVED")
		}
		return evaluated(""), nil
	})
	if _, err := fake.connect(t).Navigate(context.Background(), "https://nao.existe.invalido"); err == nil {
		t.Fatal("falha de navegação devia propagar")
	}
}

// Clique que funciona mas cuja página some depois ainda devolve o que foi
// clicado: perder essa informação faria o agente repetir o clique.
func TestClickKeepsResultWhenDescribeFails(t *testing.T) {
	chamadas := 0
	fake := newFakeChrome(t, func(method string, _ map[string]any) (any, error) {
		if method != "Runtime.evaluate" {
			return map[string]any{}, nil
		}
		chamadas++
		if chamadas == 1 {
			return evaluated("cliquei em: Entrar"), nil
		}
		return nil, errFake("Inspected target navigated or closed")
	})
	got, err := fake.connect(t).Click(context.Background(), "Entrar")
	if err != nil {
		t.Fatalf("o clique deu certo; não devia falhar por causa da descrição: %v", err)
	}
	if !strings.Contains(got, "cliquei em") {
		t.Fatalf("devia preservar o resultado do clique: %q", got)
	}
}

// Captura que não pode ser gravada precisa falhar com o motivo da escrita.
func TestScreenshotReportsWriteFailure(t *testing.T) {
	fake := newFakeChrome(t, func(method string, _ map[string]any) (any, error) {
		if method == "Page.captureScreenshot" {
			return map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("x"))}, nil
		}
		return map[string]any{}, nil
	})
	// Um arquivo no lugar do diretório torna a escrita impossível.
	arquivo := filepath.Join(t.TempDir(), "bloqueio")
	if err := os.WriteFile(arquivo, []byte("x"), 0o644); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	_, err := fake.connect(t).Screenshot(context.Background(), filepath.Join(arquivo, "captura.png"))
	if err == nil {
		t.Fatal("escrita impossível devia falhar")
	}
}

// Resposta do protocolo que não é o JSON esperado precisa virar erro, e não um
// resultado vazio que o agente trataria como página em branco.
func TestEvaluateRejectsMalformedResult(t *testing.T) {
	fake := newFakeChrome(t, func(string, map[string]any) (any, error) {
		// `result` como string, onde o protocolo manda objeto.
		return "isto não é o formato certo", nil
	})
	if _, err := fake.connect(t).ReadText(context.Background()); err == nil {
		t.Fatal("resultado malformado devia produzir erro")
	}
}

// errFake é um erro simples para o roteiro do Chrome falso.
type errFake string

// Error atende a interface de erro.
func (e errFake) Error() string { return string(e) }
