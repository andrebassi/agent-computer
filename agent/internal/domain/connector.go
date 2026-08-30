package domain

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Connector é uma forma estruturada de trabalhar com um serviço externo.
//
// A documentação recomenda preferir um conector a clicar pelo site quando ele
// existe, porque é mais confiável. O motivo prático: clicar depende de o layout
// não ter mudado, de a sessão estar viva e de o elemento estar visível; uma
// chamada de API depende só do contrato.
//
// Conectores são de CONTA, não de agente: instalar um o torna disponível a todas
// as telas. Isso segue a documentação, e traz junto a consequência dela — a
// credencial do conector fica ao alcance de qualquer agente da máquina.
type Connector struct {
	Name        string
	Description string
	// Operations são as ações que o conector expõe. Cada uma vira uma ferramenta
	// oferecida ao modelo, com o nome no formato "conector.operação".
	Operations []ConnectorOperation
	// SecretRef aponta para a credencial no cofre local do computador. O valor
	// NUNCA fica aqui, pelo mesmo motivo de SecretRequest não ter campo de valor.
	SecretRef string
}

// ConnectorOperation é uma ação exposta por um conector.
type ConnectorOperation struct {
	Name        string
	Description string
	// Schema é o JSON Schema dos parâmetros, como o modelo espera recebê-lo.
	Schema string
}

// connectorNamePattern aceita apenas letras, números, hífen e sublinhado.
//
// O nome vira parte do nome da ferramenta enviada ao modelo, e a API rejeita
// caractere fora desse conjunto. Barrar aqui troca uma recusa remota e obscura
// por um erro local e claro, no momento de instalar.
var connectorNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)

var (
	// ErrInvalidConnectorName cobre nome fora do padrão aceito pela API.
	ErrInvalidConnectorName = errors.New("nome de conector inválido")
	// ErrConnectorWithoutOperations cobre conector que não expõe nada.
	ErrConnectorWithoutOperations = errors.New("conector sem operações")
)

// NewConnector monta e valida um conector.
func NewConnector(name, description string, ops []ConnectorOperation, secretRef string) (*Connector, error) {
	if !connectorNamePattern.MatchString(name) {
		return nil, fmt.Errorf("%w: %q (use letras, números, hífen e sublinhado)", ErrInvalidConnectorName, name)
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrConnectorWithoutOperations, name)
	}
	for _, op := range ops {
		if !connectorNamePattern.MatchString(op.Name) {
			return nil, fmt.Errorf("%w: operação %q de %q", ErrInvalidConnectorName, op.Name, name)
		}
	}
	return &Connector{Name: name, Description: description, Operations: ops, SecretRef: secretRef}, nil
}

// ToolName monta o nome da ferramenta de uma operação, no formato que o modelo
// verá. O separador é ponto porque a API o aceita, e ele deixa claro de onde a
// ferramenta veio quando uma pessoa lê o histórico.
func (c *Connector) ToolName(operation string) string {
	return c.Name + "." + operation
}

// RequiresAuth diz se o conector precisa de credencial para funcionar.
func (c *Connector) RequiresAuth() bool {
	return c.SecretRef != ""
}

// TaskRequest é o que se extrai do texto que a pessoa escreveu: a tarefa em si,
// os conectores anexados com "@" e as habilidades referenciadas com "/".
//
// A documentação define essas duas sintaxes. Interpretá-las no domínio, e não no
// adaptador de linha de comando, mantém a regra num lugar só — o dia em que
// entrar uma segunda porta de entrada, ela herda o comportamento sem duplicação.
type TaskRequest struct {
	// Prompt é o texto sem os marcadores, que é o que vai ao modelo.
	Prompt string
	// Connectors são os nomes anexados com "@", sem repetição e em ordem estável.
	Connectors []string
	// Skills são os nomes referenciados com "/", sem repetição e em ordem estável.
	Skills []string
}

// mentionPattern casa "@nome" e "/nome" apenas quando o marcador começa o texto
// ou vem logo depois de um espaço.
//
// A âncora é o que evita os dois falsos positivos que importam: um endereço de
// e-mail e um caminho de arquivo. Sem ela, pedir ao agente que grave algo em
// /workspace anexaria uma habilidade chamada "workspace" e removeria o caminho
// do texto — quebrando a tarefa em silêncio.
var mentionPattern = regexp.MustCompile(`(^|\s)([@/])([a-zA-Z0-9_-]{1,48})\b`)

// ParseTaskRequest separa os marcadores do texto da tarefa.
//
// Os marcadores são REMOVIDOS do prompt: eles são instrução para o agente, não
// para o modelo. Deixá-los faria o modelo tratar o nome do conector como parte
// do pedido.
func ParseTaskRequest(text string) TaskRequest {
	seenConnectors := map[string]bool{}
	seenSkills := map[string]bool{}
	req := TaskRequest{}

	cleaned := mentionPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := mentionPattern.FindStringSubmatch(match)
		prefix, marker, name := parts[1], parts[2], parts[3]
		switch marker {
		case "@":
			if !seenConnectors[name] {
				seenConnectors[name] = true
				req.Connectors = append(req.Connectors, name)
			}
		case "/":
			if !seenSkills[name] {
				seenSkills[name] = true
				req.Skills = append(req.Skills, name)
			}
		}
		// O espaço que precedia o marcador é preservado, senão palavras vizinhas
		// grudariam uma na outra ao remover o marcador do meio da frase.
		return prefix
	})

	// Espaços duplicados sobram onde os marcadores saíram.
	req.Prompt = strings.Join(strings.Fields(cleaned), " ")
	return req
}

// SortedToolNames devolve os nomes de ferramenta de um conjunto de conectores,
// em ordem alfabética.
//
// A ordem estável importa por custo: a lista de ferramentas entra no prompt a
// cada iteração, e uma ordem que muda a cada chamada invalida o cache de prompt
// do fornecedor, fazendo pagar entrada cheia todas as vezes.
func SortedToolNames(connectors []*Connector) []string {
	names := []string{}
	for _, c := range connectors {
		for _, op := range c.Operations {
			names = append(names, c.ToolName(op.Name))
		}
	}
	sort.Strings(names)
	return names
}
