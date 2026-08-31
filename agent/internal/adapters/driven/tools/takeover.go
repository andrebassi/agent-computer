// Package tools reúne as capacidades que o agente pode exercer na máquina.
package tools

import (
	"context"
	"fmt"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// Takeover é a ferramenta com que o agente PEDE que uma pessoa assuma a tela.
//
// Existir como ferramenta, e não como texto na resposta, é o que torna o pedido
// executável: chamá-la muda o estado da tarefa para bloqueada e para o laço. Um
// agente que apenas escrevesse "preciso de ajuda" continuaria agindo no turno
// seguinte, que é justamente o comportamento que a documentação proíbe quando
// aparece senha, verificação em duas etapas, CAPTCHA ou cobrança.
type Takeover struct{}

// takeoverArgs é o formato que o modelo preenche ao pedir ajuda.
type takeoverArgs struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// NewTakeover cria a ferramenta.
func NewTakeover() *Takeover { return &Takeover{} }

// Spec descreve a ferramenta para o modelo. A descrição é deliberadamente
// enfática: o comportamento que se quer é o agente parar em vez de tentar
// adivinhar uma senha ou resolver um CAPTCHA por conta própria.
func (t *Takeover) Spec() ports.ToolSpec {
	return ports.ToolSpec{
		Name: "request_takeover",
		Description: "Pare e peça que uma pessoa assuma a tela. Use SEMPRE que " +
			"encontrar senha, passkey, verificação em duas etapas, CAPTCHA, " +
			"confirmação de pagamento ou identidade, ou um site que exija uma " +
			"pessoa. Nunca tente contornar, adivinhar ou burlar essas barreiras.",
		Schema: `{
  "type": "object",
  "properties": {
    "reason": {
      "type": "string",
      "enum": ["password","two_factor","captcha","payment_identity","human_required"],
      "description": "O tipo de barreira encontrada"
    },
    "detail": {
      "type": "string",
      "description": "Uma frase dizendo exatamente o que a pessoa precisa fazer"
    }
  },
  "required": ["reason","detail"]
}`,
	}
}

// Execute converte o pedido do modelo num BlockRequest, que faz o laço parar.
func (t *Takeover) Execute(_ context.Context, _ int, arguments string) (*ports.ToolResult, error) {
	var args takeoverArgs
	if err := decodeArgs(arguments, &args); err != nil {
		return &ports.ToolResult{Output: err.Error(), Failed: true}, nil
	}
	reason := domain.BlockReason(args.Reason)
	if !domain.ValidBlockReason(reason) {
		// Devolver a lista ao modelo é mais útil que falhar: ele corrige e
		// chama de novo na iteração seguinte.
		return &ports.ToolResult{
			Output: fmt.Sprintf("motivo %q não existe; use password, two_factor, "+
				"captcha, payment_identity ou human_required", args.Reason),
			Failed: true,
		}, nil
	}
	return &ports.ToolResult{
		Output:       fmt.Sprintf("aguardando a pessoa: %s", args.Detail),
		BlockRequest: &ports.BlockRequest{Reason: reason, Detail: args.Detail},
	}, nil
}
