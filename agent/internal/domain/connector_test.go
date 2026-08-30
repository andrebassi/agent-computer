package domain

import (
	"errors"
	"strings"
	"testing"
)

// O nome do conector vira parte do nome da ferramenta enviada ao modelo, e a
// API rejeita caractere fora de letras, números, hífen e sublinhado. Barrar na
// instalação troca uma recusa remota e obscura por um erro local e claro.
func TestNewConnectorRejectsInvalidNames(t *testing.T) {
	ops := []ConnectorOperation{{Name: "list", Description: "lista", Schema: "{}"}}
	invalid := []string{"", "com espaço", "com.ponto", "com/barra", "acentuação", strings.Repeat("x", 49)}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := NewConnector(name, "d", ops, ""); !errors.Is(err, ErrInvalidConnectorName) {
				t.Fatalf("nome %q devia ser recusado, veio %v", name, err)
			}
		})
	}
}

// Nome de operação também entra no nome da ferramenta, e sofre a mesma restrição.
func TestNewConnectorRejectsInvalidOperationNames(t *testing.T) {
	ops := []ConnectorOperation{{Name: "com espaço", Description: "d", Schema: "{}"}}
	if _, err := NewConnector("github", "d", ops, ""); !errors.Is(err, ErrInvalidConnectorName) {
		t.Fatalf("operação inválida devia ser recusada, veio %v", err)
	}
}

// Conector sem operação não expõe nada e só ocuparia espaço no catálogo.
func TestNewConnectorRejectsEmptyOperations(t *testing.T) {
	if _, err := NewConnector("github", "d", nil, ""); !errors.Is(err, ErrConnectorWithoutOperations) {
		t.Fatalf("esperava ErrConnectorWithoutOperations, veio %v", err)
	}
}

// O nome da ferramenta junta conector e operação, para uma pessoa que lê o
// histórico saber de onde a chamada veio.
func TestToolNameJoinsConnectorAndOperation(t *testing.T) {
	c, err := NewConnector("github", "d", []ConnectorOperation{{Name: "list_issues"}}, "")
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if got := c.ToolName("list_issues"); got != "github.list_issues" {
		t.Fatalf("nome de ferramenta inesperado: %q", got)
	}
}

// Conector sem referência de credencial não exige autenticação.
func TestRequiresAuthFollowsSecretRef(t *testing.T) {
	ops := []ConnectorOperation{{Name: "ping"}}
	withAuth, err := NewConnector("a", "d", ops, "token")
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	without, err := NewConnector("b", "d", ops, "")
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if !withAuth.RequiresAuth() || without.RequiresAuth() {
		t.Fatal("RequiresAuth devia seguir a presença de SecretRef")
	}
}

// O caso normal das duas sintaxes que a documentação define.
func TestParseTaskRequestExtractsMarkers(t *testing.T) {
	got := ParseTaskRequest("use @github e @jira seguindo /release para publicar")
	if len(got.Connectors) != 2 || got.Connectors[0] != "github" || got.Connectors[1] != "jira" {
		t.Fatalf("conectores errados: %v", got.Connectors)
	}
	if len(got.Skills) != 1 || got.Skills[0] != "release" {
		t.Fatalf("habilidades erradas: %v", got.Skills)
	}
	// Os marcadores saem do texto: são instrução para o agente, não para o modelo.
	for _, marcador := range []string{"@github", "@jira", "/release"} {
		if strings.Contains(got.Prompt, marcador) {
			t.Fatalf("marcador %q devia sair do prompt: %q", marcador, got.Prompt)
		}
	}
	if got.Prompt != "use e seguindo para publicar" {
		t.Fatalf("prompt inesperado: %q", got.Prompt)
	}
}

// A armadilha que mais importa: um caminho de arquivo NÃO pode virar habilidade.
//
// Sem a âncora na expressão, pedir para gravar algo em /workspace anexaria uma
// habilidade chamada "workspace" e removeria o caminho do texto — quebrando a
// tarefa em silêncio, do jeito mais difícil de diagnosticar.
func TestParseTaskRequestIgnoresFilePaths(t *testing.T) {
	texto := "grave o resultado em /workspace/projects/saida.txt e leia /etc/hosts"
	got := ParseTaskRequest(texto)
	if len(got.Skills) != 0 {
		t.Fatalf("caminho de arquivo não podia virar habilidade: %v", got.Skills)
	}
	for _, caminho := range []string{"/workspace/projects/saida.txt", "/etc/hosts"} {
		if !strings.Contains(got.Prompt, caminho) {
			t.Fatalf("o caminho %q devia continuar no prompt: %q", caminho, got.Prompt)
		}
	}
}

// Endereço de e-mail também não pode virar conector.
func TestParseTaskRequestIgnoresEmailAddresses(t *testing.T) {
	got := ParseTaskRequest("mande um resumo para alguem@exemplo.com hoje")
	if len(got.Connectors) != 0 {
		t.Fatalf("e-mail não podia virar conector: %v", got.Connectors)
	}
	if !strings.Contains(got.Prompt, "alguem@exemplo.com") {
		t.Fatalf("o endereço devia continuar no prompt: %q", got.Prompt)
	}
}

// Marcador repetido entra uma vez só: anexar o mesmo conector duas vezes
// duplicaria as ferramentas oferecidas ao modelo.
func TestParseTaskRequestDeduplicatesMarkers(t *testing.T) {
	got := ParseTaskRequest("@github abre issue e @github fecha a antiga")
	if len(got.Connectors) != 1 {
		t.Fatalf("esperava um conector, veio %v", got.Connectors)
	}
}

// Marcador no começo do texto conta, apesar de não ter espaço antes.
func TestParseTaskRequestAcceptsMarkerAtStart(t *testing.T) {
	got := ParseTaskRequest("@github liste as issues abertas")
	if len(got.Connectors) != 1 || got.Connectors[0] != "github" {
		t.Fatalf("marcador no início devia contar: %v", got.Connectors)
	}
	if got.Prompt != "liste as issues abertas" {
		t.Fatalf("prompt inesperado: %q", got.Prompt)
	}
}

// Texto sem marcador nenhum passa intacto.
func TestParseTaskRequestLeavesPlainTextAlone(t *testing.T) {
	texto := "conte quantos arquivos existem no diretorio atual"
	got := ParseTaskRequest(texto)
	if got.Prompt != texto || len(got.Connectors) != 0 || len(got.Skills) != 0 {
		t.Fatalf("texto simples foi alterado: %+v", got)
	}
}

// A proteção contra caminho não pode engolir pontuação de fim de frase: depois
// do ponto não vem letra, então "/release." continua sendo um marcador.
func TestParseTaskRequestAcceptsMarkerFollowedByPunctuation(t *testing.T) {
	cases := map[string]string{
		"ponto final":  "siga o procedimento /release.",
		"vírgula":      "use @github, depois confira",
		"interrogação": "consegue usar @jira?",
	}
	for nome, texto := range cases {
		t.Run(nome, func(t *testing.T) {
			got := ParseTaskRequest(texto)
			if len(got.Connectors)+len(got.Skills) != 1 {
				t.Fatalf("marcador com pontuação devia contar: %+v", got)
			}
		})
	}
}

// Um arquivo solto na raiz também é caminho, não habilidade.
func TestParseTaskRequestIgnoresBareFilename(t *testing.T) {
	got := ParseTaskRequest("abra /notas.txt e resuma")
	if len(got.Skills) != 0 {
		t.Fatalf("nome de arquivo não podia virar habilidade: %v", got.Skills)
	}
	if !strings.Contains(got.Prompt, "/notas.txt") {
		t.Fatalf("o arquivo devia continuar no prompt: %q", got.Prompt)
	}
}

// A ordem estável dos nomes de ferramenta importa por custo: a lista entra no
// prompt a cada iteração, e ordem que muda invalida o cache do fornecedor.
func TestSortedToolNamesIsDeterministic(t *testing.T) {
	a, err := NewConnector("zeta", "d", []ConnectorOperation{{Name: "beta"}, {Name: "alfa"}}, "")
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	b, err := NewConnector("alpha", "d", []ConnectorOperation{{Name: "gama"}}, "")
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	got := SortedToolNames([]*Connector{a, b})
	want := []string{"alpha.gama", "zeta.alfa", "zeta.beta"}
	if len(got) != len(want) {
		t.Fatalf("quantidade errada: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordem instável: %v, esperava %v", got, want)
		}
	}
}
