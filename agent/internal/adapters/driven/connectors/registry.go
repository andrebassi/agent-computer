// Package connectors carrega conectores instalados no computador e os
// transforma em ferramentas oferecidas ao modelo.
//
// Um conector é declarado num manifesto JSON, não em código: instalar um serviço
// novo não deve exigir recompilar nem reiniciar o agente. O manifesto descreve a
// API — endereço, autenticação e operações — e o registro monta as ferramentas
// a partir dele.
package connectors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// Manifest é o formato em disco de um conector.
//
// Os nomes de campo JSON são contrato com quem escreve manifesto à mão.
// contract:ok
type Manifest struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	BaseURL     string              `json:"base_url"`
	Auth        ManifestAuth        `json:"auth"`
	Operations  []ManifestOperation `json:"operations"`
}

// ManifestAuth descreve como o conector se autentica.
//
// O manifesto guarda apenas a REFERÊNCIA à credencial, nunca o valor. Um
// manifesto com segredo dentro seria copiado, versionado e compartilhado sem
// que ninguém percebesse — e conectores são de conta, visíveis a todo agente.
// contract:ok
type ManifestAuth struct {
	// Type aceita "bearer", "header", "query" ou vazio (sem autenticação).
	Type string `json:"type"`
	// SecretRef é o nome do arquivo em secrets/, sem caminho.
	SecretRef string `json:"secret_ref"`
	// HeaderName vale quando Type é "header"; QueryParam, quando é "query".
	HeaderName string `json:"header_name"`
	QueryParam string `json:"query_param"`
}

// ManifestOperation é uma ação da API exposta como ferramenta. contract:ok
type ManifestOperation struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Method      string `json:"method"`
	// Path pode conter marcadores no formato {nome}, substituídos pelos
	// parâmetros que o modelo preencher.
	Path string `json:"path"`
	// Schema é o JSON Schema dos parâmetros, embutido cru.
	Schema json.RawMessage `json:"schema"`
	// BodyParams lista quais parâmetros vão no corpo em vez da URL. Sem esta
	// separação, um POST mandaria tudo na query string, onde valores acabam em
	// log de servidor.
	BodyParams []string `json:"body_params"`
}

// Registry carrega e guarda os conectores instalados.
type Registry struct {
	root       string
	connectors map[string]*loadedConnector
}

// loadedConnector junta o que o domínio conhece com o que o adaptador precisa
// para efetivamente chamar a API.
type loadedConnector struct {
	connector *domain.Connector
	manifest  Manifest
}

// NewRegistry cria o registro e a árvore de diretórios do catálogo.
//
// Fica em /workspace de propósito: conectores são de conta e precisam sobreviver
// à reconstrução do computador, junto com o resto do estado durável.
func NewRegistry(root string) (*Registry, error) {
	for _, sub := range []string{"installed", "secrets", "available"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, fmt.Errorf("criando %s: %w", sub, err)
		}
	}
	// O diretório de segredos é fechado ao dono. Não isola agentes entre si —
	// todos rodam como o mesmo usuário, e a documentação diz que as telas não
	// são fronteira de segurança —, mas evita leitura por outra conta da máquina.
	if err := os.Chmod(filepath.Join(root, "secrets"), 0o700); err != nil {
		return nil, fmt.Errorf("restringindo secrets: %w", err)
	}
	r := &Registry{root: root, connectors: map[string]*loadedConnector{}}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Reload relê o catálogo instalado do disco.
//
// Um manifesto inválido é PULADO, e não fatal: um arquivo ruim não pode impedir
// que todos os outros conectores funcionem.
func (r *Registry) Reload() error {
	dir := filepath.Join(r.root, "installed")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	loaded := map[string]*loadedConnector{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		lc, err := loadManifest(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "aviso: conector %s ignorado: %v\n", e.Name(), err)
			continue
		}
		loaded[lc.connector.Name] = lc
	}
	r.connectors = loaded
	return nil
}

// loadManifest lê e valida um manifesto.
func loadManifest(path string) (*loadedConnector, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if m.BaseURL == "" {
		return nil, fmt.Errorf("base_url vazio")
	}
	ops := make([]domain.ConnectorOperation, 0, len(m.Operations))
	for _, op := range m.Operations {
		schema := string(op.Schema)
		if schema == "" {
			schema = `{"type":"object","properties":{}}`
		}
		ops = append(ops, domain.ConnectorOperation{
			Name: op.Name, Description: op.Description, Schema: schema,
		})
	}
	c, err := domain.NewConnector(m.Name, m.Description, ops, m.Auth.SecretRef)
	if err != nil {
		return nil, err
	}
	return &loadedConnector{connector: c, manifest: m}, nil
}

// Installed devolve os conectores instalados, em ordem alfabética.
func (r *Registry) Installed() []*domain.Connector {
	out := make([]*domain.Connector, 0, len(r.connectors))
	for _, lc := range r.connectors {
		out = append(out, lc.connector)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get devolve um conector pelo nome.
func (r *Registry) Get(name string) (*domain.Connector, bool) {
	lc, ok := r.connectors[name]
	if !ok {
		return nil, false
	}
	return lc.connector, true
}

// ToolsFor monta as ferramentas dos conectores pedidos.
//
// Só os ANEXADOS entram. É o que a sintaxe "@" da documentação significa, e tem
// efeito prático grande: a descrição de toda ferramenta vai no prompt a cada
// iteração, então oferecer o catálogo inteiro custaria token em toda chamada e
// daria ao modelo acesso a serviços que a tarefa não pediu.
func (r *Registry) ToolsFor(names []string) ([]ports.Tool, []string) {
	tools := []ports.Tool{}
	missing := []string{}
	for _, name := range names {
		lc, ok := r.connectors[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		for _, op := range lc.manifest.Operations {
			tools = append(tools, newHTTPTool(lc, op, r.secretPath(lc.manifest.Auth.SecretRef)))
		}
	}
	return tools, missing
}

// secretPath monta o caminho da credencial. Referência vazia devolve vazio, que
// a ferramenta trata como "sem autenticação".
func (r *Registry) secretPath(ref string) string {
	if ref == "" {
		return ""
	}
	// filepath.Base impede que uma referência maliciosa no manifesto escape do
	// diretório de segredos com "../../etc/shadow".
	return filepath.Join(r.root, "secrets", filepath.Base(ref))
}

// Install grava um manifesto no catálogo instalado.
func (r *Registry) Install(m Manifest) error {
	if _, err := domain.NewConnector(m.Name, m.Description, toDomainOps(m.Operations), m.Auth.SecretRef); err != nil {
		return err
	}
	if m.BaseURL == "" {
		return fmt.Errorf("base_url é obrigatório")
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(r.root, "installed", m.Name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return r.Reload()
}

// Remove tira um conector do catálogo. A credencial NÃO é apagada junto: ela
// pode ser compartilhada com outro conector, e apagá-la em cascata quebraria o
// outro sem aviso.
func (r *Registry) Remove(name string) error {
	path := filepath.Join(r.root, "installed", filepath.Base(name)+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("conector %q não está instalado", name)
		}
		return err
	}
	return r.Reload()
}

// SetSecret grava a credencial de um conector, só para o dono.
//
// O valor não passa pelo modelo nem entra em histórico nenhum — é o mesmo
// princípio do pedido de segredo: quem informa é a pessoa, e o destino é o disco.
func (r *Registry) SetSecret(ref, value string) error {
	if ref == "" {
		return fmt.Errorf("referência de segredo vazia")
	}
	path := r.secretPath(ref)
	return os.WriteFile(path, []byte(strings.TrimSpace(value)), 0o600)
}

// HasSecret diz se a credencial de um conector já foi informada, sem lê-la.
func (r *Registry) HasSecret(ref string) bool {
	if ref == "" {
		return true
	}
	_, err := os.Stat(r.secretPath(ref))
	return err == nil
}

// toDomainOps converte operações do manifesto para o domínio.
func toDomainOps(ops []ManifestOperation) []domain.ConnectorOperation {
	out := make([]domain.ConnectorOperation, 0, len(ops))
	for _, op := range ops {
		out = append(out, domain.ConnectorOperation{Name: op.Name, Description: op.Description})
	}
	return out
}
