package connectors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// schemaFor monta um esquema com as propriedades informadas.
func schemaFor(names ...string) json.RawMessage {
	props := make([]string, 0, len(names))
	for _, name := range names {
		props = append(props, `"`+name+`":{"type":"string"}`)
	}
	return json.RawMessage(`{"type":"object","properties":{` + strings.Join(props, ",") + `}}`)
}

// Parâmetro com nome errado é RECUSADO, e a mensagem lista os aceitos.
//
// É o defeito que motiva o arquivo: `{"stat":"opened"}` em vez de `state` não
// falhava — virava query string, a API remota ignorava, e a listagem voltava
// sem filtro. O modelo concluía que o filtro não funciona na API, quando só
// tinha escrito o nome errado.
func TestUnknownParameterIsRefusedWithTheValidOnes(t *testing.T) {
	declared := declaredParams(schemaFor("id", "state", "per_page"))
	err := checkParams(declared, map[string]any{"id": "1", "stat": "opened"})
	if err == nil {
		t.Fatal("parâmetro não declarado devia ser recusado")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Errorf("a mensagem devia nomear o parâmetro errado: %v", err)
	}
	for _, valid := range []string{"id", "state", "per_page"} {
		if !strings.Contains(err.Error(), valid) {
			t.Errorf("a mensagem devia listar %q: %v", valid, err)
		}
	}
}

// Parâmetro declarado passa — o outro sentido.
func TestDeclaredParametersAreAccepted(t *testing.T) {
	declared := declaredParams(schemaFor("id", "state"))
	if err := checkParams(declared, map[string]any{"id": "1", "state": "opened"}); err != nil {
		t.Fatalf("parâmetros válidos não deviam ser recusados: %v", err)
	}
}

// Esquema SEM propriedades pula a validação.
//
// Um esquema vazio significa "não sei o que é válido", não "nada é válido".
// Recusar tudo aqui quebraria manifesto antigo, e o operador nem saberia por quê.
func TestSchemaWithoutPropertiesSkipsValidation(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"object"}`),
		json.RawMessage(`{"type":"object","properties":{}}`),
		json.RawMessage(``),
		json.RawMessage(`null`),
	} {
		if declared := declaredParams(raw); declared != nil {
			t.Errorf("esquema sem propriedades devia devolver nil, veio %v", declared)
		}
		if err := checkParams(declaredParams(raw), map[string]any{"qualquer": 1}); err != nil {
			t.Errorf("sem esquema não se valida: %v", err)
		}
	}
}

// Esquema MALFORMADO não vira recusa.
//
// Quem o escreveu foi o operador; transformar o erro dele em recusa que o
// MODELO recebe faria o modelo tentar consertar algo que não está ao alcance
// dele — e gastar turnos nisso.
func TestMalformedSchemaDoesNotBlockTheCall(t *testing.T) {
	declared := declaredParams(json.RawMessage(`{"properties": "isto devia ser objeto"}`))
	if declared != nil {
		t.Fatalf("esquema malformado devia devolver nil, veio %v", declared)
	}
	if err := checkParams(declared, map[string]any{"x": 1}); err != nil {
		t.Errorf("esquema malformado não pode barrar a chamada: %v", err)
	}
}

// Chamada sem parâmetro nenhum passa.
func TestNoParametersIsFine(t *testing.T) {
	if err := checkParams(declaredParams(schemaFor("id")), map[string]any{}); err != nil {
		t.Fatalf("chamada sem parâmetro não devia falhar: %v", err)
	}
}

// A mensagem é ESTÁVEL entre execuções.
//
// A iteração de mapa em Go tem ordem aleatória; sem ordenar, o mesmo erro sai
// com texto diferente a cada rodada, e comparar duas execuções vira ruído.
func TestMessageIsStableAcrossRuns(t *testing.T) {
	declared := declaredParams(schemaFor("alpha", "beta", "gama"))
	params := map[string]any{"zeta": 1, "delta": 2, "omega": 3}

	first := checkParams(declared, params).Error()
	for i := 0; i < 20; i++ {
		if got := checkParams(declared, params).Error(); got != first {
			t.Fatalf("mensagem instável:\n%s\n%s", first, got)
		}
	}
}

// A validação está LIGADA na ferramenta, e não só disponível como função.
//
// Este teste existe por uma prova de falha que reprovou o TESTE, não o código:
// desarmar `checkParams` no `httptool` deixava os casos acima passando, porque
// todos chamam a função direto. Testar a função não prova que alguém a chama —
// foi assim que `RecordProgress` ficou escrito, testado e nunca invocado, com o
// arquivo em 0 bytes na máquina.
//
// Aqui a chamada passa pelo `Execute` de verdade, e o servidor registra se a
// requisição chegou a sair. Se a validação for removida, ela sai — e o teste
// falha.
func TestValidationIsWiredIntoTheTool(t *testing.T) {
	allowLoopbackForTest(t)
	var reached bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	registry, _ := newRegistry(t)
	installForServer(t, registry, server.URL, ManifestOperation{
		Name: "listar", Path: "/itens",
		Schema: json.RawMessage(`{"type":"object","properties":{"state":{"type":"string"}}}`),
	}, ManifestAuth{})
	tools, _ := registry.ToolsFor([]string{"teste"})
	if len(tools) == 0 {
		t.Fatal("a ferramenta devia ter sido montada")
	}

	// Nome errado: `stat` em vez de `state`.
	result, err := tools[0].Execute(context.Background(), 1, `{"stat":"opened"}`)
	if err != nil {
		t.Fatalf("não devia devolver erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("parâmetro não declarado devia marcar falha")
	}
	if reached {
		t.Error("a requisição NÃO devia ter saído — a validação vem antes da rede")
	}
	if !strings.Contains(result.Output, "stat") {
		t.Errorf("a mensagem devia nomear o parâmetro: %s", result.Output)
	}
}

// Com o parâmetro CERTO, a requisição sai — o outro sentido da fiação.
func TestValidParameterStillReachesTheAPI(t *testing.T) {
	allowLoopbackForTest(t)
	var reached bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	registry, _ := newRegistry(t)
	installForServer(t, registry, server.URL, ManifestOperation{
		Name: "listar", Path: "/itens",
		Schema: json.RawMessage(`{"type":"object","properties":{"state":{"type":"string"}}}`),
	}, ManifestAuth{})
	tools, _ := registry.ToolsFor([]string{"teste"})

	result, err := tools[0].Execute(context.Background(), 1, `{"state":"opened"}`)
	if err != nil {
		t.Fatalf("execução: %v", err)
	}
	if result.Failed {
		t.Fatalf("parâmetro válido não devia falhar: %s", result.Output)
	}
	if !reached {
		t.Error("a requisição devia ter chegado à API")
	}
}

// SecretsFor devolve a credencial dos conectores anexados, para armar a redação.
func TestSecretsForReturnsAttachedCredentials(t *testing.T) {
	registry, _ := newRegistry(t)
	installForServer(t, registry, "https://api.exemplo.com", ManifestOperation{
		Name: "op", Path: "/x",
	}, ManifestAuth{Type: "bearer", SecretRef: "teste-token"})

	if err := registry.SetSecret("teste-token", "VALOR-DE-TESTE-1234"); err != nil {
		t.Fatalf("gravando a credencial: %v", err)
	}
	secrets := registry.SecretsFor(context.Background(), []string{"teste"})
	if len(secrets) != 1 || secrets[0] != "VALOR-DE-TESTE-1234" {
		t.Fatalf("devia devolver a credencial do conector anexado: %v", secrets)
	}
}

// Conector NÃO anexado não entra — a redação segue o que a tarefa pediu.
func TestSecretsForIgnoresUnattachedConnectors(t *testing.T) {
	registry, _ := newRegistry(t)
	installForServer(t, registry, "https://api.exemplo.com", ManifestOperation{
		Name: "op", Path: "/x",
	}, ManifestAuth{Type: "bearer", SecretRef: "teste-token"})
	_ = registry.SetSecret("teste-token", "VALOR-DE-TESTE-1234")

	if secrets := registry.SecretsFor(context.Background(), []string{"outro"}); len(secrets) != 0 {
		t.Fatalf("conector não anexado não devia entrar: %v", secrets)
	}
}

// Conector SEM credencial configurada não derruba nada.
//
// É caso normal: a ferramenta reclama na hora de usar, com mensagem própria e
// melhor. Derrubar a criação da tarefa aqui trocaria um aviso útil por uma
// falha seca.
func TestSecretsForToleratesMissingCredential(t *testing.T) {
	registry, _ := newRegistry(t)
	installForServer(t, registry, "https://api.exemplo.com", ManifestOperation{
		Name: "op", Path: "/x",
	}, ManifestAuth{Type: "bearer", SecretRef: "nunca-gravado"})

	if secrets := registry.SecretsFor(context.Background(), []string{"teste"}); len(secrets) != 0 {
		t.Fatalf("sem credencial, devia vir vazio: %v", secrets)
	}
}

// Conector sem autenticação declarada também não entra.
func TestSecretsForSkipsUnauthenticatedConnectors(t *testing.T) {
	registry, _ := newRegistry(t)
	installForServer(t, registry, "https://api.exemplo.com", ManifestOperation{
		Name: "op", Path: "/x",
	}, ManifestAuth{})

	if secrets := registry.SecretsFor(context.Background(), []string{"teste"}); len(secrets) != 0 {
		t.Fatalf("conector sem auth não tem segredo: %v", secrets)
	}
}
