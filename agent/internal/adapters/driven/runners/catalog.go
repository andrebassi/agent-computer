// Package runners resolve qual agente de código executa uma delegação.
//
// O ralph tem a mesma ideia (`AGENT_CMD` com quatro CLIs alternativos), e a
// implementação dele é o que NÃO se faz aqui: uma string de shell expandida com
// `eval`. Comando montado assim é injeção de comando por configuração — e neste
// projeto o rebaixamento do modelo para o usuário `agent` depende justamente de
// nada conseguir montar uma linha de comando arbitrária.
//
// Aqui o comando é vetor, vai direto para `exec.Command` sem shell no meio, e
// sai de um catálogo FECHADO que só o `agentd` escreve.
package runners

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// ErrUnknownRunner marca pedido de runner fora do catálogo.
	//
	// Sentinela porque quem chama precisa distinguir "o modelo escolheu um nome
	// que não existe" (culpa do pedido, e a mensagem lista as opções) de "o
	// catálogo está quebrado" (culpa da máquina).
	ErrUnknownRunner = errors.New("runner não está no catálogo")
	// ErrRunnerNotInstalled marca runner cadastrado cujo binário falta.
	ErrRunnerNotInstalled = errors.New("runner cadastrado, mas o binário não está na máquina")
	// ErrInvalidCatalog marca catálogo malformado.
	ErrInvalidCatalog = errors.New("catálogo de runners inválido")
)

// promptPlaceholder é substituído pelo caminho do arquivo de prompt.
const promptPlaceholder = "{prompt}"

// safeName limita o nome de um runner.
//
// O nome vem do MODELO. Sem esta âncora, um nome com barra ou ponto-ponto
// atravessaria para lugares que o catálogo não previu quando fosse usado em
// mensagem, log ou caminho.
var safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Runner descreve como invocar um agente de código.
type Runner struct {
	// Cmd é o comando como VETOR, nunca como string.
	//
	// É a diferença que importa em relação ao ralph: vetor vai para
	// `exec.Command` sem passar por shell, então `;`, `&&` e `$(...)` dentro de
	// um argumento são texto literal, não comando.
	Cmd []string `json:"cmd"`
	// Stdin manda o prompt pela entrada padrão em vez de por argumento.
	//
	// Existe porque os CLIs divergem: uns leem o arquivo (`-f {prompt}`), outros
	// esperam o texto na entrada (`codex exec -`).
	Stdin bool `json:"stdin"`
	// Description aparece na mensagem de erro quando o runner não existe.
	Description string `json:"description"`
	// EnvFile é o arquivo de credencial DESTE runner, relativo ao diretório de
	// estado. Vazio usa o padrão do agente de código.
	//
	// Existe porque credencial não se compartilha entre fornecedores: um arquivo
	// só servindo a todos daria ao Claude Code a chave da OpenAI e ao Codex a da
	// Anthropic. Nenhum precisa da do outro — e um agente de código executa
	// comando arbitrário por desenho, então a chave que ele alcança é a chave que
	// pode sair da máquina.
	EnvFile string `json:"env_file"`
}

// Catalog é o conjunto fechado de runners disponíveis.
type Catalog struct {
	runners map[string]Runner
}

// Load lê o catálogo de um arquivo JSON.
//
// Arquivo ausente NÃO é erro: devolve um catálogo vazio, e quem não pedir runner
// nenhum continua funcionando com o padrão. É o que permite a máquina existente
// seguir igual sem o arquivo novo.
func Load(path string) (*Catalog, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Catalog{runners: map[string]Runner{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lendo o catálogo: %w", err)
	}
	return Parse(content)
}

// Parse valida e monta o catálogo a partir do JSON.
func Parse(content []byte) (*Catalog, error) {
	raw := map[string]Runner{}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	// Campo desconhecido RECUSA, em vez de ser ignorado.
	//
	// Um `"comand"` com erro de digitação viraria um runner com `Cmd` vazio, que
	// falharia na hora de executar com uma mensagem que não aponta a causa.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCatalog, err)
	}

	for name, runner := range raw {
		if !safeName.MatchString(name) {
			return nil, fmt.Errorf("%w: nome %q fora do formato permitido", ErrInvalidCatalog, name)
		}
		if len(runner.Cmd) == 0 {
			return nil, fmt.Errorf("%w: runner %q sem comando", ErrInvalidCatalog, name)
		}
		if strings.TrimSpace(runner.Cmd[0]) == "" {
			return nil, fmt.Errorf("%w: runner %q com binário vazio", ErrInvalidCatalog, name)
		}
		// Um comando que invoca shell devolve exatamente o poder que o vetor
		// existe para tirar: `sh -c "..."` reintroduz interpretação, e com ela
		// `sudo`, `;` e substituição de comando.
		if isShell(runner.Cmd[0]) {
			return nil, fmt.Errorf(
				"%w: runner %q invoca shell (%s) — o comando precisa ser o binário direto",
				ErrInvalidCatalog, name, runner.Cmd[0])
		}
	}
	return &Catalog{runners: raw}, nil
}

// isShell diz se o binário é um interpretador de comandos.
func isShell(binary string) bool {
	switch strings.ToLower(baseName(binary)) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "env", "eval", "xargs":
		return true
	}
	return false
}

// baseName devolve o último segmento de um caminho, sem depender de filepath
// para não confundir separador em mensagem de erro.
func baseName(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// Names lista os runners cadastrados, em ordem estável.
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.runners))
	for name := range c.runners {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Resolve devolve o comando pronto para um runner, com o prompt no lugar.
//
// Três recusas, e cada uma dá uma mensagem diferente de propósito — "não
// funcionou" sem dizer qual dos três casos é obriga quem lê a investigar do zero:
//
//  1. nome fora do catálogo: lista o que existe;
//  2. binário ausente na máquina: nomeia o binário que falta;
//  3. catálogo vazio: diz que nenhum runner foi cadastrado.

// EnvFileFor devolve o NOME do arquivo de credencial de um runner, ou vazio.
//
// Vazio significa "use o padrão", e não "sem credencial": quem chama decide qual
// é o padrão, porque o caminho depende do diretório de estado, que este pacote
// não conhece.
//
// `filepath.Base` confina ao diretório de credenciais. O catálogo é escrito pelo
// operador, mas um `../../etc/agentd/vault.pass` ali entregaria a senha do cofre
// a um agente de código — e um deles executa comando arbitrário.
func (c *Catalog) EnvFileFor(name string) string {
	runner, ok := c.runners[name]
	if !ok || runner.EnvFile == "" {
		return ""
	}
	return filepath.Base(runner.EnvFile)
}

// Resolve devolve o comando pronto, conforme descrito acima.
func (c *Catalog) Resolve(name, promptPath string) ([]string, bool, error) {
	if len(c.runners) == 0 {
		return nil, false, fmt.Errorf("%w: nenhum runner cadastrado nesta máquina", ErrUnknownRunner)
	}
	runner, ok := c.runners[name]
	if !ok {
		return nil, false, fmt.Errorf("%w: %q; disponíveis: %s",
			ErrUnknownRunner, name, strings.Join(c.Names(), ", "))
	}
	if _, err := exec.LookPath(runner.Cmd[0]); err != nil {
		return nil, false, fmt.Errorf("%w: %q precisa de %q no PATH",
			ErrRunnerNotInstalled, name, runner.Cmd[0])
	}

	// A substituição é por elemento do vetor, e não no comando inteiro: assim o
	// caminho do prompt não pode se transformar em argumento adicional, aconteça
	// o que acontecer com o conteúdo dele.
	resolved := make([]string, 0, len(runner.Cmd))
	for _, part := range runner.Cmd {
		resolved = append(resolved, strings.ReplaceAll(part, promptPlaceholder, promptPath))
	}
	return resolved, runner.Stdin, nil
}
