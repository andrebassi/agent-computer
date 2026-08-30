// Package secret coleta valores sigilosos da pessoa, sem que eles passem pelo
// modelo nem apareçam em lugar nenhum.
//
// Implementa o porto SecretPrompter, que estava declarado desde o começo e sem
// implementação: o tipo de domínio garante que o valor não é RETIDO, e este
// pacote garante que ele não é ECOADO nem registrado no caminho até o destino.
package secret

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// TerminalPrompter pede o valor no terminal de quem opera.
//
// Não usa a tela do agente de propósito. A tela é compartilhada, gravada em
// captura e visível por VNC a quem tiver o túnel — digitar senha ali seria
// contrariar a razão de existir do pedido de segredo.
type TerminalPrompter struct {
	in  *os.File
	out *os.File
}

// NewTerminalPrompter usa a entrada e a saída padrão.
func NewTerminalPrompter() *TerminalPrompter {
	return &TerminalPrompter{in: os.Stdin, out: os.Stderr}
}

// ErrNotATerminal indica que não há terminal para pedir o valor com o eco
// desligado.
//
// É erro, e não aviso: ler de um cano leria o valor de um script ou de um log,
// que é exatamente o caminho pelo qual segredo vaza. Melhor recusar e explicar.
var ErrNotATerminal = errors.New("sem terminal interativo para pedir o segredo com segurança")

// promptMessage monta o que aparece na tela ao pedir o valor.
//
// É função separada porque o CONTEÚDO da pergunta é a parte que importa e a que
// erra: sem o destino, um agente comprometido poderia pedir "a senha do painel"
// e mandá-la para outro lugar, e a pessoa não teria como perceber antes de
// digitar. A leitura em si depende de um terminal e não se testa em unidade;
// esta parte sim.
func promptMessage(screen int, req *domain.SecretRequest) string {
	return fmt.Sprintf(
		"\n┌─ o agente da tela %d precisa de um valor sigiloso\n"+
			"│  o quê   : %s\n"+
			"│  destino : %s\n"+
			"│  o valor NÃO vai para o modelo nem para o histórico\n"+
			"└─ digite (não aparece na tela): ",
		screen, req.Label, req.Destination)
}

// Prompt mostra o que se pede e para onde vai, e lê o valor com o eco desligado.
//
// Mostrar o DESTINO não é enfeite: sem ele, um agente comprometido poderia pedir
// "a senha do painel" e mandá-la para outro lugar, e a pessoa não teria como
// perceber antes de digitar.
func (p *TerminalPrompter) Prompt(_ context.Context, screen int, req *domain.SecretRequest) (string, error) {
	fd := int(p.in.Fd())
	if !term.IsTerminal(fd) {
		return "", ErrNotATerminal
	}

	fmt.Fprint(p.out, promptMessage(screen, req))

	raw, err := term.ReadPassword(fd)
	fmt.Fprintln(p.out)
	if err != nil {
		return "", fmt.Errorf("lendo o valor: %w", err)
	}

	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("valor vazio")
	}
	if err := req.Fulfill(value); err != nil {
		return "", err
	}
	return value, nil
}
