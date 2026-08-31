package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/ports"
	"github.com/andrebassi/agent-computer/agent/internal/secretref"
)

// httpTimeout evita que uma API lenta prenda a tarefa. É mais curto que o do
// shell porque chamada de API que passa de um minuto quase sempre é problema do
// outro lado, e o modelo se recupera melhor vendo o timeout que esperando.
const httpTimeout = 60 * time.Second

// maxResponseBytes limita o que volta ao histórico. Uma listagem de API pode
// devolver megabytes, e tudo isso viraria token de entrada cobrado a cada
// iteração seguinte.
const maxResponseBytes = 8000

// httpTool expõe uma operação de conector como ferramenta do agente.
type httpTool struct {
	connectorName string
	operation     ManifestOperation
	baseURL       string
	auth          ManifestAuth
	secretPath    string
	// secrets resolve a credencial pelo cofre, caindo para o arquivo.
	//
	// O valor NUNCA sai deste processo: o agentd monta a requisição HTTP ele
	// mesmo, então a credencial do conector não chega a nenhum subprocesso que o
	// modelo dirija.
	secrets *secretref.Resolver
	client  *http.Client
}

// defaultedSchema devolve o esquema da operação, ou um esquema vazio válido.
//
// Trata "null" além de vazio, e isso não é preciosismo: um campo json.RawMessage
// ausente vira o texto "null" depois de passar por Marshal e Unmarshal, que é o
// que acontece com todo manifesto gravado no catálogo. Sem esta checagem a
// ferramenta é anunciada com "parameters": null, a API rejeita, e o conector
// inteiro para de funcionar — com o erro apontando para o lugar errado.
// Descoberto por teste, não por leitura.
func defaultedSchema(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return `{"type":"object","properties":{}}`
	}
	return s
}

// newHTTPTool monta a ferramenta de uma operação.
func newHTTPTool(lc *loadedConnector, op ManifestOperation, secrets *secretref.Resolver, secretPath string) *httpTool {
	return &httpTool{
		connectorName: lc.connector.Name,
		operation:     op,
		baseURL:       strings.TrimRight(lc.manifest.BaseURL, "/"),
		auth:          lc.manifest.Auth,
		secretPath:    secretPath,
		secrets:       secrets,
		// Cliente com discador que valida o IP DE DESTINO na hora de conectar.
		// `validateBaseURL` recusa o IP literal do metadata, mas um NOME que
		// resolve para ele passava -- ver dialer.go.
		client: newGuardedClient(httpTimeout),
	}
}

// Spec descreve a ferramenta para o modelo, com o nome no formato
// "conector.operação".
func (h *httpTool) Spec() ports.ToolSpec {
	schema := defaultedSchema(h.operation.Schema)
	return ports.ToolSpec{
		Name:        h.connectorName + "." + h.operation.Name,
		Description: h.operation.Description,
		Schema:      schema,
		// Chamada de API é a ÚNICA ferramenta deste agente que pode rodar em
		// paralelo com as irmãs. Ela não guarda estado entre chamadas, monta a
		// requisição do zero a cada vez, e não toca em nada compartilhado — nem
		// a aba do Chrome, nem o teclado da tela, nem /workspace.
		//
		// O ganho é real: uma tarefa que consulta dois conectores no mesmo turno
		// deixa de pagar a soma das latências de rede.
		Concurrent: true,
	}
}

// Execute chama a API e devolve a resposta.
//
// Erro HTTP não vira erro de ferramenta: o código e o corpo voltam como texto,
// e o modelo decide o que fazer. Um 404 costuma significar que ele errou um
// parâmetro, e vê-lo permite corrigir na iteração seguinte — abortar não.
func (h *httpTool) Execute(ctx context.Context, _ int, arguments string) (*ports.ToolResult, error) {
	params := map[string]any{}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &params); err != nil {
			return &ports.ToolResult{Output: fmt.Sprintf("argumentos inválidos: %v", err), Failed: true}, nil
		}
	}

	// Parâmetro não declarado vira query string e a API remota o ignora — o
	// resultado volta certo em forma e errado em conteúdo. Recusar aqui devolve
	// ao modelo a lista dos aceitos, e ele corrige na iteração seguinte.
	if err := checkParams(declaredParams(h.operation.Schema), params); err != nil {
		return &ports.ToolResult{Output: err.Error(), Failed: true}, nil
	}

	path, remaining := expandPath(h.operation.Path, params)
	endpoint := h.baseURL + path

	bodyKeys := map[string]bool{}
	for _, k := range h.operation.BodyParams {
		bodyKeys[k] = true
	}

	// O que não foi consumido pelo caminho vai para o corpo ou para a query,
	// conforme o manifesto declarou.
	body := map[string]any{}
	query := url.Values{}
	for k, v := range remaining {
		if bodyKeys[k] {
			body[k] = v
			continue
		}
		query.Set(k, fmt.Sprintf("%v", v))
	}

	var reader io.Reader
	if len(body) > 0 {
		encoded, err := json.Marshal(body)
		if err != nil {
			return &ports.ToolResult{Output: fmt.Sprintf("corpo inválido: %v", err), Failed: true}, nil
		}
		reader = bytes.NewReader(encoded)
	}

	method := strings.ToUpper(h.operation.Method)
	if method == "" {
		method = http.MethodGet
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return &ports.ToolResult{Output: fmt.Sprintf("requisição inválida: %v", err), Failed: true}, nil
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	if err := h.applyAuth(ctx, req, query); err != nil {
		return &ports.ToolResult{Output: err.Error(), Failed: true}, nil
	}
	if len(query) > 0 {
		req.URL.RawQuery = query.Encode()
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return &ports.ToolResult{Output: fmt.Sprintf("falha de rede: %v", err), Failed: true}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return &ports.ToolResult{Output: fmt.Sprintf("falha lendo resposta: %v", err), Failed: true}, nil
	}
	text := truncateResponse(string(raw))

	if resp.StatusCode >= 400 {
		return &ports.ToolResult{
			Output: fmt.Sprintf("HTTP %d de %s:\n%s", resp.StatusCode, endpoint, text),
			Failed: true,
		}, nil
	}
	if text == "" {
		text = fmt.Sprintf("HTTP %d, sem corpo", resp.StatusCode)
	}
	return &ports.ToolResult{Output: text}, nil
}

// applyAuth acrescenta a credencial à requisição.
//
// O segredo é lido do disco a cada chamada, e não guardado em memória. Custa uma
// leitura de arquivo pequeno e evita que o valor fique num processo de vida
// longa, onde apareceria num dump de memória ou num core dump.
func (h *httpTool) applyAuth(ctx context.Context, req *http.Request, query url.Values) error {
	if h.auth.Type == "" || h.secretPath == "" {
		return nil
	}
	// Cofre primeiro, arquivo depois. A chave segue o nome do conector para o
	// provisionamento não precisar de uma tabela de correspondência à parte —
	// tabela é o tipo de coisa que diverge do código sem ninguém notar.
	secret, _, err := h.secrets.Value(ctx, "connectors/"+h.connectorName, h.secretPath)
	if err != nil {
		return fmt.Errorf("conector %q sem credencial configurada (agentd -connector-secret %s)",
			h.connectorName, h.auth.SecretRef)
	}

	switch h.auth.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+secret)
	case "header":
		name := h.auth.HeaderName
		if name == "" {
			name = "Authorization"
		}
		req.Header.Set(name, secret)
	case "query":
		// Credencial em query string acaba em log de servidor e em referrer.
		// É suportado porque algumas APIs só oferecem isso, mas o manifesto
		// precisa pedir explicitamente.
		param := h.auth.QueryParam
		if param == "" {
			param = "api_key"
		}
		query.Set(param, secret)
	default:
		return fmt.Errorf("tipo de autenticação desconhecido: %q", h.auth.Type)
	}
	return nil
}

// expandPath substitui os marcadores {nome} do caminho e devolve o que sobrou.
//
// Os valores são escapados com url.PathEscape: sem isso, um identificador com
// barra ou espaço montaria uma URL diferente da pretendida.
func expandPath(path string, params map[string]any) (string, map[string]any) {
	remaining := map[string]any{}
	for k, v := range params {
		remaining[k] = v
	}
	out := path
	for k, v := range params {
		marker := "{" + k + "}"
		if strings.Contains(out, marker) {
			out = strings.ReplaceAll(out, marker, url.PathEscape(fmt.Sprintf("%v", v)))
			delete(remaining, k)
		}
	}
	return out, remaining
}

// truncateResponse corta resposta longa pelo começo, preservando o início.
//
// Diferente da saída de shell, aqui o que interessa costuma estar no topo: JSON
// de API começa pelos campos relevantes, e o fim é paginação ou metadado.
func truncateResponse(s string) string {
	if len(s) <= maxResponseBytes {
		return s
	}
	return s[:maxResponseBytes] + fmt.Sprintf("\n\n[... resposta truncada em %d bytes ...]", maxResponseBytes)
}
