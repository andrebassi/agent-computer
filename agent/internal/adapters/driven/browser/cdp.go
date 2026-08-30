// Package browser fala com o Chrome da tela do agente pelo protocolo de
// depuração (CDP).
//
// É o que faltava para o agente PILOTAR o navegador, e não apenas tê-lo por
// perto. Antes desta peça, o Chrome estava de pé e a porta aberta, mas nenhuma
// ferramenta as usava: tudo que a documentação do Grok Bot descreve sobre
// navegar, logar e clicar dependia de uma pessoa fazer na tela.
//
// A conexão é sempre com 127.0.0.1: a porta de depuração dá controle total do
// navegador, incluindo ler cookie de sessão, e por isso nunca sai do loopback.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// dialTimeout limita a espera pela conexão. O Chrome é local, então demora aqui
// significa navegador travado, e esperar não melhora.
const dialTimeout = 10 * time.Second

// commandTimeout cobre o tempo de um comando CDP.
//
// É generoso porque `Page.navigate` numa página pesada leva segundos, e cortar
// cedo produziria falha em página que teria carregado.
const commandTimeout = 45 * time.Second

// maxTextBytes limita o texto devolvido ao modelo.
//
// Uma página comum tem dezenas de milhares de caracteres, e o conteúdo entra no
// histórico — cobrado a cada iteração seguinte. 6 KB comportam o miolo de uma
// página sem estourar o custo.
const maxTextBytes = 6000

// Client conversa com uma aba do Chrome.
type Client struct {
	port    int
	conn    *websocket.Conn
	lastID  atomic.Int64
	timeout time.Duration
	// settle é a pausa depois de uma ação que muda a página. É campo, e não
	// constante, só para o teste poder zerá-la: com 2 segundos por ação, uma
	// suíte de meia dúzia de casos passaria mais tempo dormindo que testando.
	settle time.Duration
}

// target é uma aba ou janela que o Chrome expõe. Os nomes de campo vêm do
// protocolo. contract:ok
type target struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// cdpResponse é a resposta de um comando. contract:ok
type cdpResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Connect abre conexão com a primeira aba de página da tela indicada.
//
// A porta segue o mesmo esquema do resto do projeto: 9220 + número da tela.
// Alvos do tipo `browser_ui` são ignorados de propósito — são a barra de
// endereço e menus internos, e conectar neles daria uma aba que não é a página.
func Connect(ctx context.Context, screen int) (*Client, error) {
	port := 9220 + screen
	targets, err := listTargets(ctx, port)
	if err != nil {
		return nil, err
	}
	var chosen *target
	for i := range targets {
		if targets[i].Type == "page" && targets[i].WebSocketDebuggerURL != "" {
			chosen = &targets[i]
			break
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("nenhuma aba de página aberta na tela %d", screen)
	}

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, chosen.WebSocketDebuggerURL, nil)
	if err != nil {
		return nil, fmt.Errorf("conectando ao navegador da tela %d: %w", screen, err)
	}
	// Uma página com muito conteúdo devolve respostas grandes; o limite padrão
	// da biblioteca cortaria a mensagem e o erro pareceria de protocolo.
	conn.SetReadLimit(32 << 20)

	return &Client{port: port, conn: conn, timeout: commandTimeout, settle: settleDelay}, nil
}

// Close encerra a conexão.
func (c *Client) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// listTargets pergunta ao Chrome quais alvos existem.
func listTargets(ctx context.Context, port int) ([]target, error) {
	reqCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("o navegador não respondeu em 127.0.0.1:%d: %w", port, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var targets []target
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, fmt.Errorf("resposta inesperada do navegador: %w", err)
	}
	return targets, nil
}

// send despacha um comando e espera a resposta correspondente.
//
// O CDP multiplexa eventos e respostas na mesma conexão, então é preciso
// descartar tudo que não tem o id pedido. Sem esse laço, um evento de página
// carregando seria lido como resposta do comando, e o resultado viria vazio.
func (c *Client) send(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	id := c.lastID.Add(1)
	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if err := c.conn.Write(callCtx, websocket.MessageText, body); err != nil {
		return nil, fmt.Errorf("enviando %s: %w", method, err)
	}
	for {
		_, data, err := c.conn.Read(callCtx)
		if err != nil {
			return nil, fmt.Errorf("lendo resposta de %s: %w", method, err)
		}
		var resp cdpResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// evaluate roda JavaScript na página e devolve o resultado como texto.
func (c *Client) evaluate(ctx context.Context, expression string) (string, error) {
	raw, err := c.send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		// Sem isto, uma expressão que devolve Promise entrega o objeto pendente
		// em vez do valor — e o sintoma é um resultado vazio sem erro.
		"awaitPromise": true,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Result struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.ExceptionDetails != nil {
		return "", fmt.Errorf("a página devolveu erro: %s", out.ExceptionDetails.Text)
	}
	value := strings.TrimSpace(string(out.Result.Value))
	// Texto vem como JSON entre aspas; devolver cru deixaria escapes visíveis.
	if strings.HasPrefix(value, `"`) {
		var s string
		if err := json.Unmarshal(out.Result.Value, &s); err == nil {
			return s, nil
		}
	}
	return value, nil
}
