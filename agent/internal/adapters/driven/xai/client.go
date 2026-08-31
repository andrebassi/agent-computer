// Package xai implementa o porto LanguageModel contra a API da xAI.
//
// O formato de requisição e resposta é compatível com o da OpenAI — os nomes de
// campo JSON aqui são valor de contrato, e renomeá-los quebra a integração.
package xai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// defaultBaseURL é o endereço da API. Fica configurável para o teste apontar
// para um servidor local em vez de gastar token de verdade.
const defaultBaseURL = "https://api.x.ai/v1"

// defaultModel foi medido com suporte a chamada de ferramenta em 29/08/2026.
const defaultModel = "grok-4.6"

// DefaultModel expõe o modelo padrão para quem precisa da CHAVE, não do cliente.
//
// Quem precisa é a tabela de preços: sem o nome, ela não acha a entrada, e o
// teto de custo fica desligado num caminho onde havia preço. Duplicar a string
// "grok-4.6" no ponto de composição criaria duas fontes de verdade que divergem
// na primeira troca de modelo.
func DefaultModel() string { return defaultModel }

// requestTimeout é generoso porque uma resposta com raciocínio longo passa de um
// minuto, e um timeout curto derrubaria justamente as tarefas difíceis.
const requestTimeout = 5 * time.Minute

// Client fala com a API da xAI.
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
	// maxAttempts e backoff são configuráveis para o TESTE poder exercitar o
	// retry sem dormir 6 segundos. Um teste lento é um teste que alguém apaga
	// em seis meses.
	maxAttempts int
	backoff     func(attempt int) time.Duration
}

// Option configura o cliente na construção.
type Option func(*Client)

// WithBaseURL troca o endereço da API, para o teste apontar a um servidor local.
func WithBaseURL(url string) Option { return func(c *Client) { c.baseURL = url } }

// WithModel troca o modelo usado nas requisições.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

// WithHTTPClient troca o cliente HTTP, para o teste controlar o transporte.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithRetry troca o teto de tentativas e a espera entre elas. Existe para o
// teste usar milissegundos onde a produção usa segundos.
func WithRetry(attempts int, wait func(attempt int) time.Duration) Option {
	return func(c *Client) { c.maxAttempts, c.backoff = attempts, wait }
}

// NewClient monta o cliente. A chave vem de fora: este pacote nunca lê o cofre
// nem variável de ambiente, para não existir caminho pelo qual ela apareça num
// log de diagnóstico.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("chave da API vazia")
	}
	c := &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		model:   defaultModel,
		http:    &http.Client{Timeout: requestTimeout},

		maxAttempts: maxAttempts,
		backoff:     backoffFor,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// chatRequest é o corpo enviado ao endpoint de conversa. contract:ok
type chatRequest struct {
	Model      string       `json:"model"`
	Messages   []apiMessage `json:"messages"`
	Tools      []apiTool    `json:"tools,omitempty"`
	ToolChoice string       `json:"tool_choice,omitempty"`
}

// apiMessage é um turno no formato da API, com os campos de chamada de
// ferramenta que o domínio mantém separados. contract:ok
type apiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []apiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// apiToolCall é uma chamada de ferramenta emitida pelo modelo. contract:ok
type apiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function apiToolFunction `json:"function"`
}

// apiToolFunction carrega o nome da ferramenta e os argumentos em JSON cru, do
// jeito que o modelo os produziu. contract:ok
type apiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// apiTool descreve uma ferramenta oferecida ao modelo. contract:ok
type apiTool struct {
	Type     string        `json:"type"`
	Function apiToolSchema `json:"function"`
}

// apiToolSchema é a descrição da ferramenta com o esquema de parâmetros, que
// segue como JSON cru para não amarrar o domínio ao formato do fornecedor.
// contract:ok
type apiToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chatResponse é a resposta da API, com o uso de tokens que alimenta o controle
// de custo. contract:ok
type chatResponse struct {
	Choices []struct {
		Message      apiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		// Detalhe do prompt: quantos tokens vieram do CACHE do fornecedor.
		//
		// Importa para o custo, e muito: o token em cache custa 0,50 por milhão
		// contra 2,00 do token novo. Este agente usa cache de propósito -- a
		// ordem estável das ferramentas existe para isso -- então ignorar o
		// campo superestimaria a conta em até QUATRO vezes, e um teto que
		// superestima quatro vezes para a tarefa cedo demais.
		//
		// Campo opcional: fornecedor que não o devolva deixa zero, e a conta
		// simplesmente cobra tudo como token novo. Errar para MAIS é o lado
		// seguro num teto.
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// Complete envia o histórico e devolve o próximo passo que o modelo quer dar.
//
// Repete a chamada em falha transitória — rede, tempo esgotado, 429, 5xx — e
// desiste nas demais. Duas consequências que quem chama precisa saber:
//
//   - esta função pode demorar VÁRIAS vezes o tempo de uma requisição, e o faz
//     segurando a trava da tela;
//   - janela de contexto estourada volta como ports.ErrContextTooLong, que NÃO
//     é falha de transporte: quem sabe reagir é o serviço, encurtando a conversa.
func (c *Client) Complete(ctx context.Context, messages []domain.Message, tools []ports.ToolSpec) (*ports.Completion, error) {
	var lastErr error
	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		out, kind, err := c.attempt(ctx, messages, tools)
		if err == nil {
			return out, nil
		}
		lastErr = err

		switch kind {
		case kindContextTooLong:
			// Sai na hora: repetir igual dá o mesmo erro, e comprimir é decisão
			// de domínio, não de transporte.
			return nil, fmt.Errorf("%w: %v", ports.ErrContextTooLong, err)
		case kindTransient:
			if attempt == c.maxAttempts-1 {
				return nil, fmt.Errorf("%w após %d tentativas: %v", ports.ErrModelUnavailable, c.maxAttempts, err)
			}
			if waitErr := sleepCtx(ctx, c.backoff(attempt)); waitErr != nil {
				// Contexto morreu durante a espera: devolve o motivo real, não
				// o erro da chamada que ia ser repetida.
				return nil, waitErr
			}
		default:
			return nil, err
		}
	}
	return nil, lastErr
}

// attempt faz UMA requisição e classifica a falha, sem repetir nada.
func (c *Client) attempt(ctx context.Context, messages []domain.Message, tools []ports.ToolSpec) (*ports.Completion, failureKind, error) {
	req := chatRequest{Model: c.model, Messages: toAPIMessages(messages)}
	if len(tools) > 0 {
		req.Tools = toAPITools(tools)
		req.ToolChoice = "auto"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, kindPermanent, fmt.Errorf("montando requisição: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, kindPermanent, fmt.Errorf("criando requisição: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Falha ANTES de haver resposta: só o transporte tem evidência aqui.
		return nil, classifyTransport(ctx, err), fmt.Errorf("chamando a API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		// Conexão cortada no meio da leitura é transitória por natureza.
		return nil, classifyTransport(ctx, err), fmt.Errorf("lendo resposta: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// O corpo do erro é truncado de propósito: a API pode devolver a
		// requisição inteira, e o histórico pode conter dado sensível.
		return nil, classifyStatus(resp.StatusCode, raw),
			fmt.Errorf("API devolveu %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, kindPermanent, fmt.Errorf("decodificando resposta: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, kindPermanent, fmt.Errorf("resposta sem choices")
	}

	choice := parsed.Choices[0]
	out := &ports.Completion{
		Content:          choice.Message.Content,
		StopReason:       choice.FinishReason,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		CachedTokens:     parsed.Usage.PromptTokensDetails.CachedTokens,
	}
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, domain.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return out, kindPermanent, nil
}

// toAPIMessages converte o histórico do domínio para o formato da API.
func toAPIMessages(messages []domain.Message) []apiMessage {
	out := make([]apiMessage, 0, len(messages))
	for _, m := range messages {
		am := apiMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			am.ToolCalls = append(am.ToolCalls, apiToolCall{
				ID:       tc.ID,
				Type:     "function",
				Function: apiToolFunction{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		out = append(out, am)
	}
	return out
}

// toAPITools converte as especificações de ferramenta para o formato da API.
func toAPITools(tools []ports.ToolSpec) []apiTool {
	out := make([]apiTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, apiTool{
			Type: "function",
			Function: apiToolSchema{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  json.RawMessage(t.Schema),
			},
		})
	}
	return out
}

// truncate encurta texto para mensagem de erro sem cortar no meio de um
// caractere multibyte, o que produziria bytes inválidos no log.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
