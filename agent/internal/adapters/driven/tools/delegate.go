package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// delegateTimeout é generoso porque trabalho de código costuma envolver ler
// vários arquivos, rodar teste e corrigir — e cortar no meio deixaria a árvore
// pela metade, que é pior que demorar.
const delegateTimeout = 15 * time.Minute

// maxDelegateOutput limita o que volta ao histórico do agente que delegou.
//
// O relatório do Claude Code pode ser longo, e ele entra no prompt de todas as
// iterações seguintes. O que interessa a quem delegou é a conclusão, não a
// narração inteira.
const maxDelegateOutput = 6000

// Delegate entrega uma tarefa de código ao Claude Code.
//
// Existe porque os dois agentes têm forças diferentes e não se substituem: este
// aqui navega, chama API e sabe parar numa barreira sensível; o Claude Code
// edita arquivo, mexe em git e abre subagentes. Sobrepõem-se em shell, e só.
//
// O caso que justifica a ferramenta é o misto — "leia o site e ajuste o código
// conforme" —, que nenhum dos dois faz sozinho.
type Delegate struct {
	// workdir é onde o Claude Code roda. Aponta para o workspace durável, e não
	// para o efêmero, porque o trabalho dele precisa sobreviver ao rebuild.
	workdir string
	// envFile guarda a credencial dele. Fica em arquivo, e não no ambiente deste
	// processo, para a chave não vazar por `ps` nem por dump de memória de um
	// processo de vida longa.
	envFile string
	// binary permite ao teste apontar para um executável falso.
	binary string
}

// delegateArgs é o formato que o modelo preenche.
type delegateArgs struct {
	Task string `json:"task"`
}

// NewDelegate cria a ferramenta de delegação.
func NewDelegate(workdir, envFile string) *Delegate {
	return &Delegate{workdir: workdir, envFile: envFile, binary: "claude"}
}

// Spec descreve a ferramenta para o modelo.
//
// A descrição diz QUANDO delegar e quando NÃO delegar. Sem o segundo, o modelo
// delega tudo — inclusive o que ele faria melhor sozinho — e a tarefa paga duas
// inferências para chegar ao mesmo lugar.
func (d *Delegate) Spec() ports.ToolSpec {
	return ports.ToolSpec{
		Name: "delegate_to_code",
		Description: "Entrega uma tarefa de CÓDIGO a um agente especializado que " +
			"edita arquivos, mexe em git e busca em repositórios. Use quando a tarefa " +
			"envolver escrever ou corrigir código, refatorar, rodar testes ou trabalhar " +
			"em vários arquivos. NÃO use para navegar, chamar API por conector, ou rodar " +
			"um comando simples — isso você faz melhor e mais barato sozinho. " +
			"Descreva a tarefa por completo: o outro agente não vê esta conversa.",
		Schema: `{
  "type": "object",
  "properties": {
    "task": {
      "type": "string",
      "description": "A tarefa de código, completa e autossuficiente. Inclua caminhos de arquivo e o critério de pronto."
    }
  },
  "required": ["task"]
}`,
	}
}

// Execute roda o Claude Code com a tarefa e devolve o relatório dele.
func (d *Delegate) Execute(ctx context.Context, _ int, arguments string) (*ports.ToolResult, error) {
	var args delegateArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return &ports.ToolResult{Output: fmt.Sprintf("argumentos inválidos: %v", err), Failed: true}, nil
	}
	if strings.TrimSpace(args.Task) == "" {
		return &ports.ToolResult{Output: "descreva a tarefa de código a delegar", Failed: true}, nil
	}

	env, err := readEnvFile(d.envFile)
	if err != nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("o agente de código não está configurado: %v", err),
			Failed: true,
		}, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, delegateTimeout)
	defer cancel()

	// -p roda sem interação e devolve só o resultado: um agente não tem como
	// responder a pergunta interativa, e sem isso a chamada penduraria até o
	// tempo limite.
	cmd := exec.CommandContext(runCtx, d.binary, "-p", args.Task)
	cmd.Dir = d.workdir
	cmd.Env = append(os.Environ(), env...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()

	output := truncateDelegateOutput(buf.String())
	if runCtx.Err() == context.DeadlineExceeded {
		return &ports.ToolResult{
			Output: fmt.Sprintf("o agente de código excedeu %s e foi interrompido. "+
				"A árvore pode ter ficado pela metade — confira antes de delegar de novo.\n\n%s",
				delegateTimeout, output),
			Failed: true,
		}, nil
	}
	if runErr != nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("o agente de código falhou (%v):\n%s", runErr, output),
			Failed: true,
		}, nil
	}
	if strings.TrimSpace(output) == "" {
		output = "(o agente de código terminou sem dizer nada)"
	}
	return &ports.ToolResult{Output: output}, nil
}

// readEnvFile lê o arquivo de ambiente da credencial, no formato CHAVE=valor.
//
// Linhas em branco e comentários são ignorados. O valor NÃO é registrado em
// lugar nenhum — nem no erro, que só diz que o arquivo falta.
func readEnvFile(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("sem arquivo de credencial configurado")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("falta a credencial em %s", path)
		}
		return nil, err
	}
	var env []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		env = append(env, line)
	}
	if len(env) == 0 {
		return nil, fmt.Errorf("o arquivo %s não tem nenhuma variável", path)
	}
	return env, nil
}

// truncateDelegateOutput corta o relatório pelo FIM, preservando o começo.
//
// Diferente da saída de shell, aqui o começo importa menos que a conclusão —
// mas o Claude Code termina com o resumo, então o corte preserva o início e
// avisa. Cortar pelo meio partiria o raciocínio ao meio.
func truncateDelegateOutput(s string) string {
	if len(s) <= maxDelegateOutput {
		return s
	}
	return s[:maxDelegateOutput] + fmt.Sprintf(
		"\n\n[... relatório truncado em %d caracteres; peça um resumo se precisar do resto ...]",
		maxDelegateOutput)
}
