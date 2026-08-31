// Package connectors carrega conectores instalados no computador e os
// transforma em ferramentas oferecidas ao modelo.
//
// Um conector é declarado num manifesto JSON, não em código: instalar um serviço
// novo não deve exigir recompilar nem reiniciar o agente. O manifesto descreve a
// API — endereço, autenticação e operações — e o registro monta as ferramentas
// a partir dele.
package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
	"github.com/andrebassi/agent-computer/agent/internal/secretref"
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
	// secrets resolve credencial de conector pelo cofre, caindo para o arquivo.
	//
	// Nulo é aceito e significa "só arquivo": é o estado de uma máquina ainda
	// não provisionada, que precisa continuar funcionando durante a migração.
	secrets *secretref.Resolver
}

// WithSecrets liga o cofre ao registro.
//
// Devolve o próprio registro para encadear na composição. A credencial resolvida
// aqui NUNCA sai do processo: o agentd monta a requisição HTTP ele mesmo, então
// ela não chega a nenhum subprocesso que o modelo dirija.
func (r *Registry) WithSecrets(store secretref.SecretGetter) *Registry {
	r.secrets = secretref.New(store)
	return r
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
	//
	// Falhar aqui NAO derruba o registro, e a diferenca importa: quem nao e dono
	// do diretorio nao consegue mudar o modo dele, e isso e o esperado depois da
	// separacao de usuarios -- `secrets` pertence ao `agentd`, e o operador roda
	// como `agent`.
	//
	// Antes era erro fatal, e o efeito era um `-catalog list` (comando de
	// LEITURA) morrer com "restringindo secrets: permission denied" numa maquina
	// perfeitamente correta. O modo ja esta certo; quem o ajusta e o dono, no
	// boot.
	if err := os.Chmod(filepath.Join(root, "secrets"), 0o700); err != nil && !os.IsPermission(err) {
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
		if e.IsDir() || !manifestExtensions[strings.ToLower(filepath.Ext(e.Name()))] {
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

// manifestExtensions são as extensões aceitas no catálogo.
//
// Os dois formatos existem porque servem a públicos diferentes: JSON é o que
// uma ferramenta gera, e YAML é o que uma pessoa escreve — ele aceita
// comentário, e manifesto de conector é exatamente o tipo de arquivo onde
// explicar "esta operação precisa do escopo repo" vale mais que a economia de
// uma linha.
var manifestExtensions = map[string]bool{".json": true, ".yaml": true, ".yml": true}

// decodeManifest lê um manifesto em JSON ou YAML.
//
// A biblioteca de YAML usada converte para JSON antes de decodificar, então as
// mesmas tags `json` dos structs valem para os dois formatos. A alternativa
// seria um parser YAML nativo, que exigiria duplicar cada tag — e tag duplicada
// diverge com o tempo, criando um campo que funciona num formato e não no outro.
func decodeManifest(data []byte, ext string) (Manifest, error) {
	var m Manifest
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &m); err != nil {
			return m, fmt.Errorf("YAML inválido: %w", err)
		}
	default:
		if err := json.Unmarshal(data, &m); err != nil {
			return m, fmt.Errorf("JSON inválido: %w", err)
		}
	}
	return m, nil
}

// loadManifest lê e valida um manifesto, em qualquer formato aceito.
func loadManifest(path string) (*loadedConnector, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := decodeManifest(data, strings.ToLower(filepath.Ext(path)))
	if err != nil {
		return nil, err
	}
	if m.BaseURL == "" {
		return nil, fmt.Errorf("base_url vazio")
	}
	// Recusa na LEITURA, e não só na instalação: um manifesto pode ter chegado
	// ao diretório por outro caminho — cópia manual, restauração de foto, ou
	// versão anterior deste código. Validar só ao instalar deixaria o já
	// instalado passar para sempre.
	if err := validateBaseURL(m.BaseURL); err != nil {
		return nil, err
	}
	ops := make([]domain.ConnectorOperation, 0, len(m.Operations))
	for _, op := range m.Operations {
		schema := defaultedSchema(op.Schema)
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
			tools = append(tools, newHTTPTool(lc, op, r.secrets, r.secretPath(lc.manifest.Auth.SecretRef)))
		}
	}
	return tools, missing
}

// SecretsFor devolve os valores de credencial dos conectores pedidos.
//
// Existe para ARMAR A REDAÇÃO, e só para isso. O mecanismo de apagar segredo do
// histórico existia inteiro e nunca protegeu nada: `TrackSecret` só era chamado
// por teste, então `Redact` percorria uma lista vazia em toda mensagem.
//
// O que se rastreia é o conjunto que o `agentd` de fato manipula durante a
// tarefa — e que pode reaparecer numa saída de comando (`env`, um log da API
// que ecoa o cabeçalho) ou no conteúdo de uma página.
//
// Erro de leitura é IGNORADO de propósito: conector sem credencial configurada
// é caso normal (a ferramenta reclama na hora de usar, com mensagem própria), e
// derrubar a criação da tarefa aqui trocaria um aviso útil por uma falha seca.
func (r *Registry) SecretsFor(ctx context.Context, names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		lc, ok := r.connectors[name]
		if !ok {
			continue
		}
		path := r.secretPath(lc.manifest.Auth.SecretRef)
		if path == "" {
			continue
		}
		value, _, err := r.secrets.Value(ctx, "connectors/"+name, path)
		if err != nil || strings.TrimSpace(value) == "" {
			continue
		}
		out = append(out, value)
	}
	return out
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
	if err := validateBaseURL(m.BaseURL); err != nil {
		return err
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

// InstallFile instala a partir de um arquivo, PRESERVANDO o formato original.
//
// É o caminho de quem escreveu um manifesto YAML à mão: converter para JSON na
// instalação apagaria justamente os comentários que motivaram escolher YAML.
func (r *Registry) InstallFile(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	if !manifestExtensions[ext] {
		return fmt.Errorf("formato não reconhecido: %q (use .json, .yaml ou .yml)", ext)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m, err := decodeManifest(data, ext)
	if err != nil {
		return err
	}
	// Valida ANTES de copiar: um manifesto quebrado no catálogo vira um aviso
	// no arranque de toda tarefa, e ninguém liga para aviso repetido.
	if _, err := domain.NewConnector(m.Name, m.Description, toDomainOps(m.Operations), m.Auth.SecretRef); err != nil {
		return err
	}
	if m.BaseURL == "" {
		return fmt.Errorf("base_url é obrigatório")
	}
	if err := validateBaseURL(m.BaseURL); err != nil {
		return err
	}
	// Um conector já instalado noutro formato precisa sair, senão os dois
	// arquivos coexistem e o vencedor depende da ordem de leitura do diretório.
	_, _ = r.removeManifestFiles(m.Name)
	dest := filepath.Join(r.root, "installed", m.Name+ext)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return err
	}
	return r.Reload()
}

// Remove tira um conector do catálogo. A credencial NÃO é apagada junto: ela
// pode ser compartilhada com outro conector, e apagá-la em cascata quebraria o
// outro sem aviso.
func (r *Registry) Remove(name string) error {
	removed, err := r.removeManifestFiles(name)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("conector %q não está instalado", name)
	}
	return r.Reload()
}

// removeManifestFiles apaga o manifesto do conector em qualquer formato aceito,
// e diz se algo foi removido.
func (r *Registry) removeManifestFiles(name string) (bool, error) {
	found := false
	for ext := range manifestExtensions {
		path := filepath.Join(r.root, "installed", filepath.Base(name)+ext)
		if err := os.Remove(path); err == nil {
			found = true
		} else if !os.IsNotExist(err) {
			return found, err
		}
	}
	return found, nil
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

// SecretStatus diz o que se sabe sobre a credencial de um conector.
type SecretStatus int

const (
	// SecretNotRequired marca conector que dispensa credencial.
	SecretNotRequired SecretStatus = iota
	// SecretPresent marca credencial encontrada e legível.
	SecretPresent
	// SecretMissing marca credencial que de fato não existe.
	SecretMissing
	// SecretUnknown marca credencial que pode existir, mas está fora do alcance
	// de quem perguntou.
	SecretUnknown
)

// CheckSecret separa "não existe" de "não posso ver".
//
// O diretório de segredos é `agentd:agentd 0700`, então o usuário do modelo
// recebe `permission denied` num arquivo que existe. Tratar isso como ausência é
// falso alarme na direção mais cara: manda consertar o que está intacto.
//
// Medido em 31/08/2026: o passo 6 do deploy roda `-catalog list` como `agent` e
// imprimia "⚠️ CREDENCIAL FALTANDO" para um token gravado e funcionando, a cada
// implantação — enquanto a permissão restritiva era justamente a contenção
// operando como projetada.
func (r *Registry) CheckSecret(ref string) SecretStatus {
	if ref == "" {
		return SecretNotRequired
	}
	_, err := os.Stat(r.secretPath(ref))
	switch {
	case err == nil:
		return SecretPresent
	case os.IsPermission(err):
		return SecretUnknown
	default:
		return SecretMissing
	}
}

// toDomainOps converte operações do manifesto para o domínio.
func toDomainOps(ops []ManifestOperation) []domain.ConnectorOperation {
	out := make([]domain.ConnectorOperation, 0, len(ops))
	for _, op := range ops {
		out = append(out, domain.ConnectorOperation{Name: op.Name, Description: op.Description})
	}
	return out
}
