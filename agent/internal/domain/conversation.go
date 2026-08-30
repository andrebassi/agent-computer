package domain

import (
	"errors"
	"fmt"
)

// Role é o papel de quem falou num turno da conversa. Os nomes seguem o formato
// que a API da xAI usa, compatível com o da OpenAI — é valor de contrato:
// renomear quebra a integração. contract:ok
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall é uma chamada de ferramenta pedida pelo modelo.
type ToolCall struct {
	ID   string
	Name string
	// Arguments chega como JSON cru, do jeito que o modelo emitiu. Não é
	// decodificado aqui: o domínio não sabe quais ferramentas existem, e cada
	// adaptador conhece o próprio formato de argumento.
	Arguments string
}

// Message é um turno da conversa.
type Message struct {
	Role      Role
	Content   string
	ToolCalls []ToolCall
	// ToolCallID amarra um resultado à chamada que o originou. Obrigatório
	// quando Role é RoleTool: sem ele a API rejeita o turno inteiro.
	ToolCallID string
}

// Conversation é o histórico de uma tarefa.
//
// Mora no domínio, e não no adaptador do modelo, porque as regras que valem
// aqui são de produto e não do fornecedor: segredo nunca entra, e o histórico
// tem limite para não crescer sem fim.
type Conversation struct {
	TaskID   string
	Messages []Message
	// secrets guarda valores a apagar antes de qualquer envio. Fica não
	// exportado para não ser serializado por acidente ao gravar o estado.
	secrets []string
}

// ErrToolResultWithoutID sinaliza resultado de ferramenta sem a chamada de origem.
var ErrToolResultWithoutID = errors.New("resultado de ferramenta sem ToolCallID")

// NewConversation começa uma conversa com a instrução de sistema.
func NewConversation(taskID, systemPrompt string) *Conversation {
	return &Conversation{
		TaskID:   taskID,
		Messages: []Message{{Role: RoleSystem, Content: systemPrompt}},
	}
}

// TrackSecret registra um valor a ser apagado daqui em diante. Chamado quando a
// pessoa preenche um pedido de segredo, para que o valor suma caso reapareça na
// saída de um comando ou no conteúdo de uma página.
func (c *Conversation) TrackSecret(value string) {
	if len(value) < 4 {
		return
	}
	c.secrets = append(c.secrets, value)
}

// AddUser acrescenta uma fala da pessoa, já com segredos apagados.
func (c *Conversation) AddUser(content string) {
	c.Messages = append(c.Messages, Message{Role: RoleUser, Content: Redact(content, c.secrets)})
}

// AddAssistant acrescenta a resposta do modelo, com as chamadas de ferramenta
// que ele pediu.
func (c *Conversation) AddAssistant(content string, calls []ToolCall) {
	c.Messages = append(c.Messages, Message{
		Role:      RoleAssistant,
		Content:   Redact(content, c.secrets),
		ToolCalls: calls,
	})
}

// AddToolResult acrescenta o resultado de uma ferramenta.
//
// É o ponto de entrada mais perigoso do histórico: aqui chega saída de shell e
// conteúdo de página, que é justamente onde um segredo ecoado apareceria. Por
// isso a limpeza acontece antes de a mensagem existir.
func (c *Conversation) AddToolResult(toolCallID, content string) error {
	if toolCallID == "" {
		return ErrToolResultWithoutID
	}
	c.Messages = append(c.Messages, Message{
		Role:       RoleTool,
		Content:    Redact(content, c.secrets),
		ToolCallID: toolCallID,
	})
	return nil
}

// Trim limita o histórico a maxMessages, preservando SEMPRE a instrução de
// sistema, que é a primeira mensagem.
//
// Cortar do começo sem essa ressalva removeria justamente as regras de conduta
// do agente, e o efeito prático seria ele voltar a fazer o que foi proibido
// depois de algumas dezenas de turnos.
//
// O corte também não pode deixar um resultado de ferramenta órfão: a API recusa
// um turno RoleTool cujo assistant correspondente saiu do histórico. Por isso a
// varredura avança até a primeira mensagem que não seja RoleTool.
func (c *Conversation) Trim(maxMessages int) {
	if maxMessages < 2 || len(c.Messages) <= maxMessages {
		return
	}
	system := c.Messages[0]
	// Quantas mensagens, além da de sistema, cabem.
	keep := maxMessages - 1
	start := len(c.Messages) - keep
	for start < len(c.Messages) && c.Messages[start].Role == RoleTool {
		start++
	}
	trimmed := make([]Message, 0, keep+1)
	trimmed = append(trimmed, system)
	trimmed = append(trimmed, c.Messages[start:]...)
	c.Messages = trimmed
}

// Summary devolve uma linha por mensagem, para diagnóstico. Usa o conteúdo já
// limpo de segredos, então é seguro imprimir.
func (c *Conversation) Summary() string {
	out := ""
	for i, m := range c.Messages {
		content := m.Content
		if len(content) > 60 {
			content = content[:60] + "..."
		}
		out += fmt.Sprintf("%d. %s: %s", i, m.Role, content)
		if len(m.ToolCalls) > 0 {
			out += fmt.Sprintf(" [%d chamada(s)]", len(m.ToolCalls))
		}
		out += "\n"
	}
	return out
}
