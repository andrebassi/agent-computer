package secret

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// Sem terminal, o pedido é RECUSADO em vez de ler de onde der.
//
// É a garantia central deste pacote: ler de um cano leria o valor de um script
// ou de um log, que é exatamente o caminho pelo qual segredo vaza. Num teste
// automatizado a entrada nunca é terminal, então este caso é o que sempre roda.
func TestPromptRefusesWithoutTerminal(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe falhou: %v", err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()

	p := &TerminalPrompter{in: reader, out: writer}
	req, err := domain.NewSecretRequest("s1", "senha do painel", "painel.exemplo.com")
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}

	value, err := p.Prompt(context.Background(), 1, req)
	if !errors.Is(err, ErrNotATerminal) {
		t.Fatalf("esperava ErrNotATerminal, veio %v", err)
	}
	if value != "" {
		t.Fatalf("nenhum valor devia ser devolvido: %q", value)
	}
	if req.Fulfilled {
		t.Fatal("o pedido não devia ficar marcado como atendido")
	}
}

// O construtor padrão usa a entrada padrão e escreve os avisos no stderr.
//
// O stderr é deliberado: a saída padrão pode estar sendo canalizada para um
// arquivo, e o pedido de senha ficaria invisível para quem deveria respondê-lo.
func TestNewTerminalPrompterUsesStdinAndStderr(t *testing.T) {
	p := NewTerminalPrompter()
	if p.in != os.Stdin {
		t.Fatal("devia ler da entrada padrão")
	}
	if p.out != os.Stderr {
		t.Fatal("devia escrever os avisos no stderr, não na saída padrão")
	}
}
