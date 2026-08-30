package connectors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleManifest devolve um manifesto válido para os testes.
func sampleManifest(name string) Manifest {
	return Manifest{
		Name:        name,
		Description: "conector de teste",
		BaseURL:     "https://api.exemplo.com",
		Auth:        ManifestAuth{Type: "bearer", SecretRef: name + "-token"},
		Operations: []ManifestOperation{{
			Name:        "list_items",
			Description: "lista itens",
			Method:      "GET",
			Path:        "/items",
			Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		}},
	}
}

// newRegistry cria um registro num diretório temporário.
func newRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry falhou: %v", err)
	}
	return r, dir
}

// Instalar e listar: o ciclo básico do catálogo.
func TestInstallAndList(t *testing.T) {
	r, _ := newRegistry(t)
	if err := r.Install(sampleManifest("github")); err != nil {
		t.Fatalf("Install falhou: %v", err)
	}
	installed := r.Installed()
	if len(installed) != 1 || installed[0].Name != "github" {
		t.Fatalf("catálogo inesperado: %+v", installed)
	}
	if _, ok := r.Get("github"); !ok {
		t.Fatal("conector instalado devia ser encontrado")
	}
}

// O catálogo é devolvido em ordem alfabética, para a listagem não dançar entre
// execuções.
func TestInstalledIsSorted(t *testing.T) {
	r, _ := newRegistry(t)
	for _, name := range []string{"zeta", "alpha", "meio"} {
		if err := r.Install(sampleManifest(name)); err != nil {
			t.Fatalf("Install falhou: %v", err)
		}
	}
	got := r.Installed()
	want := []string{"alpha", "meio", "zeta"}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("ordem instável: %s na posição %d, esperava %s", got[i].Name, i, want[i])
		}
	}
}

// Manifesto sem base_url não tem para onde chamar.
func TestInstallRejectsManifestWithoutBaseURL(t *testing.T) {
	r, _ := newRegistry(t)
	m := sampleManifest("github")
	m.BaseURL = ""
	if err := r.Install(m); err == nil {
		t.Fatal("manifesto sem base_url devia ser recusado")
	}
}

// Nome inválido é barrado na instalação, e não na primeira chamada à API.
func TestInstallRejectsInvalidName(t *testing.T) {
	r, _ := newRegistry(t)
	m := sampleManifest("nome com espaço")
	if err := r.Install(m); err == nil {
		t.Fatal("nome inválido devia ser recusado")
	}
}

// Um manifesto corrompido não pode derrubar o catálogo inteiro: seria um
// arquivo ruim impedindo todos os outros conectores de funcionar.
func TestReloadSkipsCorruptManifest(t *testing.T) {
	r, dir := newRegistry(t)
	if err := r.Install(sampleManifest("bom")); err != nil {
		t.Fatalf("Install falhou: %v", err)
	}
	corrupt := filepath.Join(dir, "installed", "quebrado.json")
	if err := os.WriteFile(corrupt, []byte("{isso não é json"), 0o644); err != nil {
		t.Fatalf("escrita falhou: %v", err)
	}
	if err := r.Reload(); err != nil {
		t.Fatalf("manifesto corrompido não devia derrubar o Reload: %v", err)
	}
	if len(r.Installed()) != 1 {
		t.Fatalf("o conector válido devia sobreviver: %+v", r.Installed())
	}
}

// Remover tira do catálogo; remover o que não existe é erro claro.
func TestRemove(t *testing.T) {
	r, _ := newRegistry(t)
	if err := r.Install(sampleManifest("github")); err != nil {
		t.Fatalf("Install falhou: %v", err)
	}
	if err := r.Remove("github"); err != nil {
		t.Fatalf("Remove falhou: %v", err)
	}
	if len(r.Installed()) != 0 {
		t.Fatal("catálogo devia ficar vazio")
	}
	if err := r.Remove("nao-existe"); err == nil {
		t.Fatal("remover inexistente devia produzir erro")
	}
}

// Só os conectores ANEXADOS viram ferramenta. É o que a sintaxe "@" significa,
// e evita pagar token pela descrição do catálogo inteiro a cada iteração.
func TestToolsForOnlyReturnsAttachedConnectors(t *testing.T) {
	r, _ := newRegistry(t)
	for _, name := range []string{"github", "jira"} {
		if err := r.Install(sampleManifest(name)); err != nil {
			t.Fatalf("Install falhou: %v", err)
		}
	}
	tools, missing := r.ToolsFor([]string{"github"})
	if len(tools) != 1 {
		t.Fatalf("só o conector anexado devia virar ferramenta, veio %d", len(tools))
	}
	if len(missing) != 0 {
		t.Fatalf("nada devia faltar: %v", missing)
	}
	if got := tools[0].Spec().Name; got != "github.list_items" {
		t.Fatalf("nome de ferramenta inesperado: %q", got)
	}
}

// Conector pedido mas não instalado é reportado, e não some em silêncio.
func TestToolsForReportsMissingConnectors(t *testing.T) {
	r, _ := newRegistry(t)
	tools, missing := r.ToolsFor([]string{"inexistente"})
	if len(tools) != 0 {
		t.Fatalf("não devia haver ferramenta: %d", len(tools))
	}
	if len(missing) != 1 || missing[0] != "inexistente" {
		t.Fatalf("o conector faltante devia ser reportado: %v", missing)
	}
}

// A credencial é gravada só para o dono, e o registro sabe dizer se ela existe
// sem precisar lê-la.
func TestSecretIsStoredWithRestrictivePermissions(t *testing.T) {
	r, dir := newRegistry(t)
	if r.HasSecret("github-token") {
		t.Fatal("credencial não devia existir ainda")
	}
	if err := r.SetSecret("github-token", "  valor-secreto  "); err != nil {
		t.Fatalf("SetSecret falhou: %v", err)
	}
	if !r.HasSecret("github-token") {
		t.Fatal("credencial devia ser encontrada")
	}
	info, err := os.Stat(filepath.Join(dir, "secrets", "github-token"))
	if err != nil {
		t.Fatalf("stat falhou: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credencial devia ser 0600, veio %o", perm)
	}
	// O valor é gravado sem espaços em volta: um "\n" colado do terminal
	// quebraria o cabeçalho de autorização.
	data, err := os.ReadFile(filepath.Join(dir, "secrets", "github-token"))
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if string(data) != "valor-secreto" {
		t.Fatalf("valor devia ser aparado: %q", string(data))
	}
}

// Conector sem autenticação conta como "credencial satisfeita", senão a
// verificação barraria conectores de API pública.
func TestHasSecretIsTrueWhenNoAuthNeeded(t *testing.T) {
	r, _ := newRegistry(t)
	if !r.HasSecret("") {
		t.Fatal("conector sem autenticação não devia exigir credencial")
	}
}

// Referência de segredo com subida de diretório não pode escapar da pasta de
// credenciais — o manifesto pode vir de fora.
func TestSecretPathCannotEscapeDirectory(t *testing.T) {
	r, dir := newRegistry(t)
	if err := r.SetSecret("../../fuga", "x"); err != nil {
		t.Fatalf("SetSecret falhou: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets", "fuga")); err != nil {
		t.Fatalf("o arquivo devia ficar dentro de secrets/: %v", err)
	}
}

// installForServer instala um conector apontando para um servidor de teste.
func installForServer(t *testing.T, r *Registry, url string, op ManifestOperation, auth ManifestAuth) {
	t.Helper()
	m := Manifest{
		Name: "teste", Description: "d", BaseURL: url,
		Auth: auth, Operations: []ManifestOperation{op},
	}
	if err := r.Install(m); err != nil {
		t.Fatalf("Install falhou: %v", err)
	}
}

// O caminho normal de uma chamada: parâmetro no caminho, resposta de volta.
func TestHTTPToolCallsAPIAndReturnsBody(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seenPath = req.URL.Path
		_, _ = io.WriteString(w, `{"items":["a","b"]}`)
	}))
	defer server.Close()

	r, _ := newRegistry(t)
	installForServer(t, r, server.URL, ManifestOperation{
		Name: "get_repo", Method: "GET", Path: "/repos/{owner}/{repo}",
		Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{})

	tools, _ := r.ToolsFor([]string{"teste"})
	res, err := tools[0].Execute(context.Background(), 1, `{"owner":"andre","repo":"agent"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if seenPath != "/repos/andre/agent" {
		t.Fatalf("caminho montado errado: %q", seenPath)
	}
	if !strings.Contains(res.Output, "items") {
		t.Fatalf("resposta não voltou: %q", res.Output)
	}
	if res.Failed {
		t.Fatal("chamada bem-sucedida não devia ser marcada como falha")
	}
}

// Parâmetro que não entra no caminho vai para a query, e o que o manifesto
// declarar como corpo vai no corpo — valores em query acabam em log de servidor.
func TestHTTPToolSeparatesQueryFromBody(t *testing.T) {
	var seenQuery, seenBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seenQuery = req.URL.RawQuery
		b, _ := io.ReadAll(req.Body)
		seenBody = string(b)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	r, _ := newRegistry(t)
	installForServer(t, r, server.URL, ManifestOperation{
		Name: "create", Method: "POST", Path: "/issues",
		BodyParams: []string{"title"},
		Schema:     json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{})

	tools, _ := r.ToolsFor([]string{"teste"})
	if _, err := tools[0].Execute(context.Background(), 1, `{"title":"um bug","state":"open"}`); err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if !strings.Contains(seenBody, "um bug") {
		t.Fatalf("title devia ir no corpo: %q", seenBody)
	}
	if !strings.Contains(seenQuery, "state=open") {
		t.Fatalf("state devia ir na query: %q", seenQuery)
	}
}

// A credencial vira cabeçalho de autorização.
func TestHTTPToolAppliesBearerAuth(t *testing.T) {
	var seenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seenAuth = req.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	r, _ := newRegistry(t)
	installForServer(t, r, server.URL, ManifestOperation{
		Name: "ping", Method: "GET", Path: "/ping", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{Type: "bearer", SecretRef: "tok"})
	if err := r.SetSecret("tok", "credencial-de-teste"); err != nil {
		t.Fatalf("SetSecret falhou: %v", err)
	}

	tools, _ := r.ToolsFor([]string{"teste"})
	if _, err := tools[0].Execute(context.Background(), 1, `{}`); err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if seenAuth != "Bearer credencial-de-teste" {
		t.Fatalf("cabeçalho de autorização errado: %q", seenAuth)
	}
}

// Sem credencial configurada, a mensagem precisa dizer COMO configurar — e não
// deixar o modelo tentando de novo sem saber o que falta.
func TestHTTPToolReportsMissingCredential(t *testing.T) {
	r, _ := newRegistry(t)
	installForServer(t, r, "https://api.exemplo.com", ManifestOperation{
		Name: "ping", Method: "GET", Path: "/ping", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{Type: "bearer", SecretRef: "faltante"})

	tools, _ := r.ToolsFor([]string{"teste"})
	res, err := tools[0].Execute(context.Background(), 1, `{}`)
	if err != nil {
		t.Fatalf("credencial ausente não devia virar erro de execução: %v", err)
	}
	if !res.Failed || !strings.Contains(res.Output, "credencial") {
		t.Fatalf("a mensagem devia explicar a credencial faltante: %q", res.Output)
	}
}

// Erro HTTP volta como texto para o modelo decidir. Um 404 costuma significar
// parâmetro errado, e vê-lo permite corrigir na iteração seguinte.
func TestHTTPToolReturnsHTTPErrorAsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}))
	defer server.Close()

	r, _ := newRegistry(t)
	installForServer(t, r, server.URL, ManifestOperation{
		Name: "get", Method: "GET", Path: "/nada", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{})

	tools, _ := r.ToolsFor([]string{"teste"})
	res, err := tools[0].Execute(context.Background(), 1, `{}`)
	if err != nil {
		t.Fatalf("erro HTTP não devia virar erro de execução: %v", err)
	}
	if !res.Failed || !strings.Contains(res.Output, "404") {
		t.Fatalf("a saída devia trazer o código HTTP: %q", res.Output)
	}
}

// Argumento malformado vem do modelo com alguma frequência.
func TestHTTPToolHandlesMalformedArguments(t *testing.T) {
	r, _ := newRegistry(t)
	installForServer(t, r, "https://api.exemplo.com", ManifestOperation{
		Name: "get", Method: "GET", Path: "/x", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{})

	tools, _ := r.ToolsFor([]string{"teste"})
	res, err := tools[0].Execute(context.Background(), 1, `{quebrado`)
	if err != nil {
		t.Fatalf("não devia virar erro de execução: %v", err)
	}
	if !res.Failed {
		t.Fatal("argumento inválido devia falhar")
	}
}

// Autenticação por cabeçalho próprio, que várias APIs usam no lugar de Bearer.
func TestHTTPToolAppliesCustomHeaderAuth(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = req.Header.Get("X-Api-Key")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	r, _ := newRegistry(t)
	installForServer(t, r, server.URL, ManifestOperation{
		Name: "ping", Method: "GET", Path: "/p", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{Type: "header", HeaderName: "X-Api-Key", SecretRef: "k"})
	if err := r.SetSecret("k", "chave"); err != nil {
		t.Fatalf("SetSecret falhou: %v", err)
	}
	tools, _ := r.ToolsFor([]string{"teste"})
	if _, err := tools[0].Execute(context.Background(), 1, `{}`); err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if seen != "chave" {
		t.Fatalf("cabeçalho próprio não foi aplicado: %q", seen)
	}
}

// Credencial em query string acaba em log de servidor. É suportado porque
// algumas APIs só oferecem isso, mas o manifesto precisa pedir explicitamente.
func TestHTTPToolAppliesQueryAuth(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = req.URL.Query().Get("api_key")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	r, _ := newRegistry(t)
	installForServer(t, r, server.URL, ManifestOperation{
		Name: "ping", Method: "GET", Path: "/p", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{Type: "query", SecretRef: "k"})
	if err := r.SetSecret("k", "chave-na-query"); err != nil {
		t.Fatalf("SetSecret falhou: %v", err)
	}
	tools, _ := r.ToolsFor([]string{"teste"})
	if _, err := tools[0].Execute(context.Background(), 1, `{}`); err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if seen != "chave-na-query" {
		t.Fatalf("credencial não foi para a query: %q", seen)
	}
}

// Tipo de autenticação desconhecido precisa falhar com mensagem clara, e não
// mandar a requisição sem credencial.
func TestHTTPToolRejectsUnknownAuthType(t *testing.T) {
	r, _ := newRegistry(t)
	installForServer(t, r, "https://api.exemplo.com", ManifestOperation{
		Name: "ping", Method: "GET", Path: "/p", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{Type: "invencionice", SecretRef: "k"})
	if err := r.SetSecret("k", "x"); err != nil {
		t.Fatalf("SetSecret falhou: %v", err)
	}
	tools, _ := r.ToolsFor([]string{"teste"})
	res, err := tools[0].Execute(context.Background(), 1, `{}`)
	if err != nil {
		t.Fatalf("não devia virar erro de execução: %v", err)
	}
	if !res.Failed || !strings.Contains(res.Output, "autenticação") {
		t.Fatalf("devia explicar o tipo desconhecido: %q", res.Output)
	}
}

// Servidor fora do ar vira falha tratada, com a mensagem chegando ao modelo.
func TestHTTPToolHandlesNetworkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := server.URL
	server.Close()

	r, _ := newRegistry(t)
	installForServer(t, r, url, ManifestOperation{
		Name: "ping", Method: "GET", Path: "/p", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{})
	tools, _ := r.ToolsFor([]string{"teste"})
	res, err := tools[0].Execute(context.Background(), 1, `{}`)
	if err != nil {
		t.Fatalf("não devia virar erro de execução: %v", err)
	}
	if !res.Failed || !strings.Contains(res.Output, "rede") {
		t.Fatalf("devia reportar falha de rede: %q", res.Output)
	}
}

// Operação sem método declarado usa GET, que é o caso mais comum de leitura.
func TestHTTPToolDefaultsToGet(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = req.Method
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	r, _ := newRegistry(t)
	installForServer(t, r, server.URL, ManifestOperation{
		Name: "ping", Path: "/p", Schema: json.RawMessage(`{"type":"object"}`),
	}, ManifestAuth{})
	tools, _ := r.ToolsFor([]string{"teste"})
	if _, err := tools[0].Execute(context.Background(), 1, `{}`); err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if seen != http.MethodGet {
		t.Fatalf("método padrão devia ser GET, veio %s", seen)
	}
}

// Operação sem esquema declarado ganha um esquema vazio válido: a API rejeita
// ferramenta sem parameters.
func TestManifestOperationWithoutSchemaGetsDefault(t *testing.T) {
	r, _ := newRegistry(t)
	installForServer(t, r, "https://api.exemplo.com", ManifestOperation{
		Name: "ping", Method: "GET", Path: "/p",
	}, ManifestAuth{})
	tools, _ := r.ToolsFor([]string{"teste"})
	if len(tools) != 1 {
		t.Fatalf("esperava uma ferramenta, veio %d", len(tools))
	}
	if !strings.Contains(tools[0].Spec().Schema, "object") {
		t.Fatalf("esquema padrão inesperado: %q", tools[0].Spec().Schema)
	}
}

// Conector inexistente devolve o segundo valor falso, para quem chama
// distinguir ausência de erro.
func TestGetReturnsFalseForUnknownConnector(t *testing.T) {
	r, _ := newRegistry(t)
	if _, ok := r.Get("nunca-instalado"); ok {
		t.Fatal("conector inexistente não devia ser encontrado")
	}
}

// Referência de segredo vazia é erro de quem chamou.
func TestSetSecretRejectsEmptyRef(t *testing.T) {
	r, _ := newRegistry(t)
	if err := r.SetSecret("", "valor"); err == nil {
		t.Fatal("referência vazia devia ser recusada")
	}
}

// Valor com barra no caminho precisa ser escapado, senão monta uma URL
// diferente da pretendida.
func TestExpandPathEscapesValues(t *testing.T) {
	got, rest := expandPath("/repos/{owner}", map[string]any{"owner": "a/b", "extra": 1})
	if got != "/repos/a%2Fb" {
		t.Fatalf("valor não foi escapado: %q", got)
	}
	if _, existe := rest["owner"]; existe {
		t.Fatal("parâmetro consumido pelo caminho não devia sobrar")
	}
	if _, existe := rest["extra"]; !existe {
		t.Fatal("parâmetro não usado no caminho devia sobrar")
	}
}

// Resposta longa é cortada; aqui o que interessa está no começo, ao contrário
// da saída de shell.
func TestTruncateResponseKeepsHead(t *testing.T) {
	long := strings.Repeat("A", maxResponseBytes+500)
	got := truncateResponse(long)
	if len(got) >= len(long) {
		t.Fatal("resposta longa devia ser cortada")
	}
	if !strings.HasPrefix(got, "AAAA") || !strings.Contains(got, "truncada") {
		t.Fatalf("corte inesperado: %q", got[:40])
	}
	if short := truncateResponse("curta"); short != "curta" {
		t.Fatalf("resposta curta foi alterada: %q", short)
	}
}
