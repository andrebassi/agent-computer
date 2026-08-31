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
	// EnvFileFor devolve o arquivo de credencial do runner, ou vazio para o padrão.
	EnvFileFor(name string) string
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

	// Credencial DESTE runner, não a do padrão.
	//
	// Sem isto, o mesmo arquivo iria para todos: o Codex receberia a chave da
	// Anthropic e o Claude Code a da OpenAI. Nenhum precisa da do outro, e um
	// agente de código executa comando arbitrário por desenho — a chave que ele
	// alcança é a chave que pode sair da máquina.
	if named := d.runners.EnvFileFor(args.Runner); named != "" {
		own, readErr := readEnvFile(filepath.Join(filepath.Dir(d.envFile), named))
		if readErr != nil {
			return &ports.ToolResult{
				Output: fmt.Sprintf("o runner %q está cadastrado mas sem credencial (%s): %v",
					args.Runner, named, readErr),
				Failed: true,
			}, nil
		}
		env = own
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

	// HOME PRÓPRIO por runner, e criado antes de rodar.
	//
	// Sem isto o CLI escreve na casa de quem chamou. Medido em 31/08/2026: o
	// Codex tentou `/root/.codex/config.toml` e falhou com "Permission denied" —
	// o processo é rebaixado para `agent`, que não escreve em /root, e o CLI não
	// tinha para onde ir.
	//
	// Um diretório por runner, e não um compartilhado: cada CLI guarda sessão,
	// cache e às vezes credencial no HOME, e misturá-los faria a configuração de
	// um aparecer para o outro.
	home := filepath.Join(filepath.Dir(d.envFile), "runner-home", args.Runner)
	// 0770, e não 0700: quem cria é o `agentd` e quem escreve é o `agent`. O
	// diretório pai tem setgid, então o filho herda o grupo `agent` — sem os dois
	// juntos o CLI leva "permission denied" num diretório que existe.
	if err := os.MkdirAll(home, 0o770); err != nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("não consegui preparar o diretório do runner %q: %v", args.Runner, err),
			Failed: true,
		}, nil
	}
	// Chmod DEPOIS do MkdirAll, e não é redundante: `MkdirAll` aplica o umask do
	// processo, e com o umask usual (022) o 0770 vira 0750 — o grupo perde a
	// escrita justamente para o usuário que vai rodar o CLI.
	//
	// O sintoma foi "EACCES: permission denied, mkdir .../opencode/.local", com o
	// diretório pai existindo e aparentemente correto.
	if err := os.Chmod(home, 0o770); err != nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("não consegui ajustar o diretório do runner %q: %v", args.Runner, err),
			Failed: true,
		}, nil
	}

	// As variáveis que ESTE runner precisa atravessarem o sudo. Só elas: a
	// credencial de um runner não tem por que chegar a outro.
	names := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if idx := strings.Index(entry, "="); idx > 0 {
			names = append(names, entry[:idx])
		}
	}
	names = append(names, "HOME", "XDG_CONFIG_HOME")

	cmd := d.sandbox.CommandPreserving(ctx, names, argv[0], argv[1:]...)
	cmd.Dir = d.workdir
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"CLAUDE_CONFIG_DIR="+d.configDir)
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
