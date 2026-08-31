package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/runners"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// RunnerResolver entrega o comando de um agente de código alternativo.
//
// É interface, e não o tipo concreto, para a ferramenta não depender do pacote
// de catálogo — e para o teste poder devolver um comando controlado sem montar
// arquivo JSON.
type RunnerResolver interface {
	// Resolve devolve o comando pronto, se o runner existe e está instalado.
	Resolve(name, promptPath string) ([]string, bool, error)
	// Names lista o que está cadastrado, para a mensagem de erro.
	Names() []string
}

// WithRunners liga a delegação a um catálogo de agentes alternativos.
//
// Sem isto, pedir um runner devolve erro dizendo que a máquina não tem catálogo
// — que é o comportamento certo para uma instalação que nunca cadastrou nenhum.
func WithRunners(catalog RunnerResolver) DelegateOption {
	return func(d *Delegate) {
		if catalog != nil {
			d.runners = catalog
		}
	}
}

// runAlternate executa a delegação por um runner do catálogo.
//
// O prompt vai em ARQUIVO, e não na linha de comando, por dois motivos que se
// somam: tarefa de código é longa e estoura o limite de argumentos do sistema, e
// argumento é visível em `ps` para qualquer usuário da máquina — inclusive o
// usuário do modelo, que é justamente de quem se quer separar o conteúdo.
func (d *Delegate) runAlternate(ctx context.Context, args delegateArgs, env []string) (*ports.ToolResult, error) {
	if d.runners == nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("esta máquina não tem catálogo de runners; "+
				"remova o campo \"runner\" para usar o agente padrão (%s)", filepath.Base(d.binary)),
			Failed: true,
		}, nil
	}

	promptFile, cleanup, err := writePromptFile(args.Task)
	if err != nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("não consegui preparar o prompt: %v", err),
			Failed: true,
		}, nil
	}
	defer cleanup()

	argv, useStdin, err := d.runners.Resolve(args.Runner, promptFile)
	if err != nil {
		// Recusa de catálogo volta como TEXTO ao modelo, não como erro de
		// execução: ele escolheu um nome, e a mensagem já traz a lista dos que
		// existem — é informação suficiente para ele se corrigir sozinho.
		return &ports.ToolResult{Output: err.Error(), Failed: true}, nil
	}

	cmd := d.sandbox.Command(ctx, argv[0], argv[1:]...)
	cmd.Dir = d.workdir
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+d.configDir)
	cmd.Env = append(cmd.Env, env...)
	if useStdin {
		cmd.Stdin = strings.NewReader(args.Task)
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	raw := buf.String()

	if ctx.Err() == context.DeadlineExceeded {
		return &ports.ToolResult{
			Output: fmt.Sprintf("o runner %q excedeu %s e foi interrompido. "+
				"A árvore pode ter ficado pela metade — confira antes de delegar de novo.\n\n%s",
				args.Runner, delegateTimeout, truncateDelegateOutput(raw)),
			Failed: true,
		}, nil
	}
	if runErr != nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("o runner %q falhou: %v\n\n%s",
				args.Runner, runErr, truncateDelegateOutput(raw)),
			Failed: true,
		}, nil
	}

	// Saída CRUA, sem tentar interpretar.
	//
	// Só o Claude Code tem `--output-format json`; os outros devolvem texto
	// livre, cada um no seu formato. Fingir que há estrutura aqui daria um
	// analisador que acerta num runner e mente nos outros — e mentir sobre
	// sucesso é o defeito exato que o `--output-format json` do padrão existe
	// para evitar.
	return &ports.ToolResult{
		Output: fmt.Sprintf("runner %q terminou.\n\n%s", args.Runner, truncateDelegateOutput(raw)),
	}, nil
}

// writePromptFile grava a tarefa num arquivo temporário legível só pelo dono.
//
// 0600 porque o texto da tarefa pode citar caminho interno, nome de sistema ou
// contexto que não interessa a outros usuários da máquina.
func writePromptFile(task string) (string, func(), error) {
	handle, err := os.CreateTemp("", "delegate-prompt-*.md")
	if err != nil {
		return "", func() {}, fmt.Errorf("criando arquivo de prompt: %w", err)
	}
	name := handle.Name()
	cleanup := func() { _ = os.Remove(name) }

	if err := handle.Chmod(0o600); err != nil {
		_ = handle.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("ajustando permissão: %w", err)
	}
	if _, err := handle.WriteString(task); err != nil {
		_ = handle.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("gravando o prompt: %w", err)
	}
	if err := handle.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("fechando o arquivo: %w", err)
	}
	return name, cleanup, nil
}

// assertCatalogSatisfiesResolver amarra o catálogo real à interface daqui.
//
// Sem esta linha, uma mudança de assinatura no catálogo só apareceria no ponto
// de composição, longe do lugar que define o contrato.
var _ RunnerResolver = (*runners.Catalog)(nil)
