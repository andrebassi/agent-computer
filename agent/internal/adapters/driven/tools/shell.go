package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// shellTimeout evita que um comando interativo trave a tarefa para sempre.
// Comando que espera entrada nunca retorna, e sem teto o agente ficaria parado
// sem nada indicar o motivo.
const shellTimeout = 2 * time.Minute

// maxOutputBytes limita o que volta ao histórico. Um `find /` despejaria
// megabytes de token de entrada, cobrados a cada iteração seguinte.
const maxOutputBytes = 8000

// Shell executa comandos na máquina do agente.
type Shell struct {
	// workdir é o diretório inicial. Aponta para o workspace durável, e não
	// para o efêmero, porque é onde o trabalho deve sobreviver a um rebuild.
	workdir string
}

// shellArgs é o formato que o modelo preenche.
type shellArgs struct {
	Command string `json:"command"`
	Workdir string `json:"workdir"`
}

// NewShell cria a ferramenta com o diretório de trabalho padrão.
func NewShell(workdir string) *Shell { return &Shell{workdir: workdir} }

// Spec descreve a ferramenta para o modelo.
func (s *Shell) Spec() ports.ToolSpec {
	return ports.ToolSpec{
		Name: "shell",
		Description: "Executa um comando no shell da máquina do agente. " +
			"Guarde resultados duráveis em /workspace; /scratch é apagado a " +
			"cada reconstrução do computador.",
		Schema: `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "O comando a executar"},
    "workdir": {"type": "string", "description": "Diretório de trabalho (opcional)"}
  },
  "required": ["command"]
}`,
	}
}

// Execute roda o comando e devolve a saída combinada.
//
// Saída diferente de zero NÃO é erro da ferramenta: `grep` sem resultado já
// devolve 1, e tratar isso como falha faria o agente abortar tarefas normais. O
// código de saída vai no texto, e o modelo decide o que fazer.
func (s *Shell) Execute(ctx context.Context, _ int, arguments string) (*ports.ToolResult, error) {
	var args shellArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return &ports.ToolResult{Output: fmt.Sprintf("argumentos inválidos: %v", err), Failed: true}, nil
	}
	if strings.TrimSpace(args.Command) == "" {
		return &ports.ToolResult{Output: "comando vazio", Failed: true}, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, shellTimeout)
	defer cancel()

	// `bash -c`, NÃO `bash -lc`. O shell de login carrega o perfil do usuário, e
	// qualquer mensagem que ele imprima — um `echo` de boas-vindas, um erro de
	// arquivo faltando — contamina a saída de TODO comando. Isso vai parar no
	// histórico enviado ao modelo, gastando token e confundindo o agente.
	// Medido nesta máquina: um `.bash_profile` com caminho quebrado fazia
	// `true` devolver uma mensagem de erro em vez de saída vazia.
	cmd := exec.CommandContext(runCtx, "bash", "-c", args.Command)
	cmd.Dir = s.workdir
	if args.Workdir != "" {
		cmd.Dir = args.Workdir
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	output := truncateOutput(buf.String())
	if runCtx.Err() == context.DeadlineExceeded {
		return &ports.ToolResult{
			Output: fmt.Sprintf("comando excedeu %s e foi interrompido. Saída parcial:\n%s", shellTimeout, output),
			Failed: true,
		}, nil
	}
	if err != nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("saída (com erro: %v):\n%s", err, output),
			Failed: true,
		}, nil
	}
	if output == "" {
		output = "(sem saída)"
	}
	return &ports.ToolResult{Output: output}, nil
}

// truncateOutput corta saída longa pelo MEIO, preservando começo e fim.
//
// Cortar só o fim é pior na prática: numa saída longa, a mensagem de erro que
// interessa costuma estar na última linha, e é justamente ela que se perderia.
func truncateOutput(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	half := maxOutputBytes / 2
	head := s[:half]
	tail := s[len(s)-half:]
	return fmt.Sprintf("%s\n\n[... %d bytes omitidos do meio ...]\n\n%s",
		head, len(s)-maxOutputBytes, tail)
}
