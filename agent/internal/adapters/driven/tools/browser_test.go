package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// São seis operações, e a contagem é conferida porque acrescentar uma sem
// registrar no construtor a deixaria invisível para o modelo — falha silenciosa
// que só apareceria como "o agente não sabe fazer isso".
func TestNewBrowserToolsExposesEveryOperation(t *testing.T) {
	tools := NewBrowserTools(t.TempDir())
	if len(tools) != 6 {
		t.Fatalf("esperava 6 operações, veio %d", len(tools))
	}
	esperados := map[string]bool{
		"browser_navigate": false, "browser_read": false, "browser_links": false,
		"browser_click": false, "browser_fill": false, "browser_screenshot": false,
	}
	for _, tool := range tools {
		nome := tool.Spec().Name
		if _, existe := esperados[nome]; !existe {
			t.Fatalf("ferramenta inesperada: %s", nome)
		}
		esperados[nome] = true
	}
	for nome, achou := range esperados {
		if !achou {
			t.Fatalf("ferramenta ausente: %s", nome)
		}
	}
}

// A descrição do preenchimento precisa PROIBIR senha em voz alta.
//
// É a única barreira que existe do lado do modelo: a recusa no código impede
// o campo `type=password`, mas nada impediria o modelo de digitar uma senha num
// campo de texto comum se a descrição não dissesse para não fazer isso.
func TestFillDescriptionForbidsSecrets(t *testing.T) {
	for _, tool := range NewBrowserTools(t.TempDir()) {
		if tool.Spec().Name != "browser_fill" {
			continue
		}
		desc := tool.Spec().Description
		for _, termo := range []string{"NUNCA", "senha", "request_takeover"} {
			if !strings.Contains(desc, termo) {
				t.Fatalf("a descrição devia conter %q: %q", termo, desc)
			}
		}
		return
	}
	t.Fatal("browser_fill não encontrada")
}

// A descrição da navegação precisa ensinar a conferir o endereço final.
//
// Um redirecionamento para página de login é a diferença entre "carregou" e
// "preciso de uma pessoa", e sem esta instrução o agente concluiria que chegou
// onde queria.
func TestNavigateDescriptionWarnsAboutRedirect(t *testing.T) {
	for _, tool := range NewBrowserTools(t.TempDir()) {
		if tool.Spec().Name != "browser_navigate" {
			continue
		}
		if !strings.Contains(tool.Spec().Description, "request_takeover") {
			t.Fatalf("a descrição devia orientar sobre redirecionamento para login: %q",
				tool.Spec().Description)
		}
		return
	}
	t.Fatal("browser_navigate não encontrada")
}

// Todo esquema precisa ser JSON válido do tipo object: a API recusa a ferramenta
// inteira se não for, e o erro aponta para a requisição, não para o esquema.
func TestBrowserSchemasAreValid(t *testing.T) {
	for _, tool := range NewBrowserTools(t.TempDir()) {
		spec := tool.Spec()
		if !strings.Contains(spec.Schema, `"type":"object"`) {
			t.Fatalf("%s: esquema não é object: %q", spec.Name, spec.Schema)
		}
		if spec.Description == "" {
			t.Fatalf("%s: sem descrição — o modelo não saberia quando usar", spec.Name)
		}
	}
}

// Sem navegador de pé, a ferramenta devolve erro tratado em vez de derrubar a
// tarefa. Num teste não há Chrome na porta, então este é o caminho que sempre
// roda — e é também o que acontece de verdade quando o navegador cai.
func TestBrowserToolFailsGracefullyWithoutBrowser(t *testing.T) {
	// Cada ferramenta recebe argumentos VÁLIDOS, senão ela falharia na validação
	// e o teste não chegaria a exercitar o caminho do navegador ausente.
	argumentos := map[string]string{
		"browser_navigate":   `{"url":"https://exemplo.invalido"}`,
		"browser_click":      `{"label":"Entrar"}`,
		"browser_fill":       `{"field":"login","value":"x"}`,
		"browser_read":       `{}`,
		"browser_links":      `{}`,
		"browser_screenshot": `{}`,
	}
	for _, tool := range NewBrowserTools(t.TempDir()) {
		// A tela 9 não tem navegador em teste nenhum.
		result, err := tool.Execute(context.Background(), 9, argumentos[tool.Spec().Name])
		if err != nil {
			t.Fatalf("%s: navegador ausente não devia virar erro de execução: %v", tool.Spec().Name, err)
		}
		if !result.Failed {
			t.Fatalf("%s: devia marcar falha", tool.Spec().Name)
		}
		if !strings.Contains(result.Output, "navegador") {
			t.Fatalf("%s: a mensagem devia explicar que o navegador não respondeu: %q",
				tool.Spec().Name, result.Output)
		}
	}
}

// Cada operação precisa exigir o argumento que lhe falta, com mensagem própria.
//
// Sem isso, uma chamada sem `url` viraria navegação para string vazia — que o
// Chrome aceita e leva a `about:blank`, deixando o agente convencido de que
// navegou.
func TestBrowserToolsValidateRequiredArguments(t *testing.T) {
	casos := map[string]struct {
		tool     string
		args     string
		esperado string
	}{
		"navegar sem url":     {"browser_navigate", `{}`, "url"},
		"clicar sem rótulo":   {"browser_click", `{}`, "rótulo"},
		"preencher sem campo": {"browser_fill", `{"value":"x"}`, "campo"},
	}
	tools := NewBrowserTools(t.TempDir())
	for nome, caso := range casos {
		t.Run(nome, func(t *testing.T) {
			var alvo = findTool(t, tools, caso.tool)
			// Tela 9: sem navegador, mas a validação de argumento acontece ANTES
			// de tentar conectar — é o que este caso verifica.
			result, err := alvo.Execute(context.Background(), 9, caso.args)
			if err != nil {
				t.Fatalf("não devia virar erro de execução: %v", err)
			}
			if !result.Failed {
				t.Fatal("devia falhar por argumento ausente")
			}
			if !strings.Contains(result.Output, caso.esperado) {
				t.Fatalf("a mensagem devia citar %q: %q", caso.esperado, result.Output)
			}
		})
	}
}

// findTool localiza uma ferramenta pelo nome, falhando claro se ela sumir.
func findTool(t *testing.T, tools []ports.Tool, nome string) ports.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Spec().Name == nome {
			return tool
		}
	}
	t.Fatalf("ferramenta %q não encontrada", nome)
	return nil
}

// As operações que não pedem argumento precisam aceitar corpo vazio: o modelo
// manda `{}` para elas, e recusar seria quebrar o caminho normal.
func TestBrowserToolsAcceptEmptyArguments(t *testing.T) {
	tools := NewBrowserTools(t.TempDir())
	for _, nome := range []string{"browser_read", "browser_links", "browser_screenshot"} {
		alvo := findTool(t, tools, nome)
		for _, args := range []string{"", "{}"} {
			result, err := alvo.Execute(context.Background(), 9, args)
			if err != nil {
				t.Fatalf("%s: não devia virar erro de execução: %v", nome, err)
			}
			// Falha aqui é do navegador ausente, não do argumento — a mensagem
			// prova qual dos dois.
			if !strings.Contains(result.Output, "navegador") {
				t.Fatalf("%s com args %q: devia falhar pelo navegador, veio %q", nome, args, result.Output)
			}
		}
	}
}

// Argumento malformado vem do modelo com alguma frequência e não pode derrubar
// a tarefa.
func TestBrowserToolHandlesMalformedArguments(t *testing.T) {
	tools := NewBrowserTools(t.TempDir())
	result, err := tools[0].Execute(context.Background(), 9, `{quebrado`)
	if err != nil {
		t.Fatalf("não devia virar erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("argumento inválido devia falhar")
	}
}
