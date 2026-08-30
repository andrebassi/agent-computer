package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// allowedDelegateTools é o que o agente de código pode usar sem perguntar.
//
// Sem esta lista ele PARA e pede aprovação — e devolve o pedido como texto
// normal, com código de saída zero, que quem delegou lê como se fosse a
// resposta. É pior que um erro, porque não parece falha nenhuma.
//
// A lista é deliberadamente curta: pesquisar, ler e editar arquivo, rodar
// comando. Não entra nada que fale com serviço externo em nome da conta — este
// computador guarda credencial de conta, e delegar não é procuração.
var allowedDelegateTools = []string{
	"WebSearch", "WebFetch", // a razão de a busca funcionar de um IP bloqueado
	"Read", "Write", "Edit", "Glob", "Grep",
	"Bash",
}

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
	//
	// Aceita os dois modos de autenticação sem distinguir: `ANTHROPIC_API_KEY`
	// para conta de API, ou `CLAUDE_CODE_OAUTH_TOKEN` para assinatura. Quem
	// escreve o arquivo escolhe — a ferramenta só repassa ao ambiente do filho.
	envFile string
	// configDir é onde o agente de código guarda sessão e configuração.
	//
	// Precisa apontar para o volume durável. O padrão dele é `~/.claude`, que
	// mora no disco do SISTEMA: um `update` destruiria a sessão junto com o
	// droplet, e a autenticação teria de ser refeita a cada rebuild.
	configDir string
	// binary permite ao teste apontar para um executável falso.
	binary string
}

// maxDelegateCostUSD é o teto de gasto de UMA delegação.
//
// Existe porque o agente de código decide sozinho quantos turnos gastar, e uma
// tarefa mal descrita pode virar uma escavação cara sem nada a mostrar. O teto
// corta e devolve `error_max_budget_usd`, que é diagnóstico — diferente de
// descobrir o gasto na fatura.
//
// 5 dólares é generoso para trabalho de código real e ainda assim uma ordem de
// grandeza abaixo do que uma fuga custaria.
const maxDelegateCostUSD = "5.00"

// delegateArgs é o formato que o modelo preenche.
type delegateArgs struct {
	Task string `json:"task"`
}

// delegateEvent é o que `--output-format json` devolve: uma lista de eventos,
// e o que interessa é o último, de tipo `result`.
//
// A saída em TEXTO não distingue resposta de recusa — foi assim que um pedido
// de permissão ("preciso que você aprove WebSearch") chegou como se fosse a
// resposta, com código de saída zero. Aqui `IsError` diz.
type delegateEvent struct {
	Type     string  `json:"type"`
	Subtype  string  `json:"subtype"`
	IsError  bool    `json:"is_error"`
	Result   string  `json:"result"`
	NumTurns int     `json:"num_turns"`
	CostUSD  float64 `json:"total_cost_usd"`
	Session  string  `json:"session_id"`
}

// NewDelegate cria a ferramenta de delegação.
//
// O diretório de configuração é derivado do arquivo de credencial, que já mora
// no volume durável — assim os dois ficam juntos e não há um terceiro caminho
// para alguém configurar errado.
func NewDelegate(workdir, envFile string) *Delegate {
	return &Delegate{
		workdir:   workdir,
		envFile:   envFile,
		configDir: filepath.Join(filepath.Dir(envFile), "claude-config"),
		binary:    "claude",
	}
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
	//
	// --allowedTools é o que impede o modo de falha mais traiçoeiro desta
	// ferramenta. Medido em 30/08: sem ele, o agente de código devolveu "preciso
	// que você aprove a permissão de WebSearch" — texto, não erro, com código de
	// saída zero. Quem delegou leu aquilo como resposta, concluiu que a busca era
	// impossível, e gastou 20 chamadas raspando HTML à mão para chegar a nada.
	//
	// A lista é explícita, e não `--permission-mode bypassPermissions`, porque o
	// que se quer liberar é PESQUISA e EDIÇÃO — não uma procuração para tudo num
	// computador que carrega credencial de conta.
	//
	// --output-format json troca texto solto por resultado estruturado. É o que
	// permite distinguir resposta de recusa, e o que traz custo e sessão — em
	// texto, os três eram invisíveis.
	cmd := exec.CommandContext(runCtx, d.binary,
		"--allowedTools", strings.Join(allowedDelegateTools, ","),
		"--output-format", "json",
		"--max-budget-usd", maxDelegateCostUSD,
		"-p", args.Task)
	cmd.Dir = d.workdir
	// A ordem importa: o que vem depois vence. O arquivo de credencial fica por
	// último para poder sobrescrever qualquer variável herdada deste processo —
	// uma `ANTHROPIC_API_KEY` velha no ambiente venceria o token de assinatura
	// e a delegação falharia com erro de saldo, apontando para o lugar errado.
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+d.configDir)
	cmd.Env = append(cmd.Env, env...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()

	raw := buf.String()
	if runCtx.Err() == context.DeadlineExceeded {
		return &ports.ToolResult{
			Output: fmt.Sprintf("o agente de código excedeu %s e foi interrompido. "+
				"A árvore pode ter ficado pela metade — confira antes de delegar de novo.\n\n%s",
				delegateTimeout, truncateDelegateOutput(raw)),
			Failed: true,
		}, nil
	}

	result, parseErr := parseDelegateResult(raw)
	if parseErr != nil {
		// Degradação segura: sem JSON legível, devolve o bruto em vez de sumir
		// com o que o agente disse. Um erro de infraestrutura antes de o agente
		// subir (binário faltando, credencial recusada) sai em texto puro.
		return &ports.ToolResult{
			Output: fmt.Sprintf("o agente de código não devolveu resultado legível (%v):\n%s",
				parseErr, truncateDelegateOutput(raw)),
			Failed: true,
		}, nil
	}

	// O relatório de custo vai junto SEMPRE, inclusive no sucesso: sem ele, o
	// preço de delegar só aparece na fatura, e a decisão de delegar de novo é
	// tomada sem saber quanto custou a anterior.
	cost := fmt.Sprintf("[%d turno(s), US$ %.4f]", result.NumTurns, result.CostUSD)

	if result.IsError || runErr != nil {
		reason := result.Subtype
		if reason == "" {
			reason = fmt.Sprintf("%v", runErr)
		}
		return &ports.ToolResult{
			Output: fmt.Sprintf("o agente de código falhou (%s) %s:\n%s",
				reason, cost, truncateDelegateOutput(result.Result)),
			Failed: true,
		}, nil
	}

	output := strings.TrimSpace(result.Result)
	if output == "" {
		output = "(o agente de código terminou sem dizer nada)"
	}
	return &ports.ToolResult{Output: truncateDelegateOutput(output) + "\n\n" + cost}, nil
}

// parseDelegateResult acha o evento `result` na saída de `--output-format json`.
//
// A saída é uma LISTA de eventos, e o que interessa é o último — os anteriores
// são inicialização e turnos intermediários. Percorrer até o fim, em vez de
// pegar o primeiro, é o que faz a função continuar certa quando a lista cresce.
func parseDelegateResult(raw string) (*delegateEvent, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("saída vazia")
	}
	var events []delegateEvent
	if err := json.Unmarshal([]byte(trimmed), &events); err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "result" {
			return &events[i], nil
		}
	}
	return nil, fmt.Errorf("nenhum evento de tipo result em %d evento(s)", len(events))
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
