// Package vault lê credenciais de um cofre gopass cifrado.
//
// Implementa o porto ports.SecretStore. Substitui o arquivo em claro que ficava
// no volume durável: o volume é fotografado por `task snapshot`, e a foto vai
// para a conta do DigitalOcean, onde a chave da xAI ficava legível para quem
// tivesse o token da conta.
//
// # Onde cada peça mora, e por quê
//
//	/workspace/agent/vault/     store cifrado   volume durável, entra na foto
//	/etc/agentd/gopass/         identidade age  disco do sistema, NÃO entra
//
// A separação é o mecanismo inteiro. Identidade junto do store na mesma partição
// tornaria a foto autossuficiente, e o ganho seria zero.
//
// # O que a biblioteca NÃO faz
//
// api.New apenas ABRE store existente — criar exige o CLI do gopass. Por isso o
// store nasce no Mac, onde o `pass` de verdade já mora, e viaja cifrado. O
// droplet nunca precisa do binário do gopass, o que preserva o modelo de um
// único binário estático em /workspace.
package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gopasspw/gopass/pkg/gopass"
	"github.com/gopasspw/gopass/pkg/gopass/api"
)

// ErrSecretNotFound indica chave ausente no cofre.
//
// É distinto de falha de abertura de propósito: chave ausente se corrige
// gravando o segredo, cofre ilegível se corrige reprovisionando a identidade.
// Uma mensagem só faria procurar no lugar errado.
var ErrSecretNotFound = errors.New("segredo não encontrado no cofre")

// ErrInvalidKey indica nome de chave que escaparia do store.
var ErrInvalidKey = errors.New("nome de chave inválido")

// opaqueError carrega a causa sem deixá-la chegar ao texto renderizado.
//
// Existe por uma regra que este pacote não pode quebrar: adapter de segredo NÃO
// interpola texto de erro de terceiro em mensagem que vai para log. O texto do
// gopass é escrito por código que não é nosso, e um dia pode citar o valor que
// tentou decifrar — logs vazam por caminhos que ninguém revisa.
//
// A causa continua alcançável por errors.Is e errors.Unwrap, para quem estiver
// depurando de verdade; ela só não vai junto no Error().
type opaqueError struct {
	summary string
	cause   error
}

// Error devolve apenas o resumo escrito por nós.
func (e *opaqueError) Error() string { return e.summary }

// Unwrap expõe a causa para errors.Is e errors.As.
func (e *opaqueError) Unwrap() error { return e.cause }

// secretReader é a fatia mínima do gopass que este adapter consome.
//
// Existe para o teste não precisar de um store cifrado de verdade em disco: a
// interface cheia de gopass.Store tem dez métodos, e um dublê teria de
// implementar todos para exercitar um só.
type secretReader interface {
	Get(ctx context.Context, name, revision string) (gopass.Secret, error)
}

// Gopass lê segredos de um store gopass já inicializado.
type Gopass struct {
	store secretReader
	// unlock injeta no contexto o callback que decifra a identidade age.
	//
	// Fica AQUI, e não a cargo de quem chama, por um defeito que só apareceu na
	// máquina: a decifragem não acontece ao abrir o store, e sim a cada leitura.
	// Passar o callback apenas para api.New deixava todo Get cair no pinentry
	// interativo, e o serviço morria em laço com "pinentry: unexpected response"
	// — com a senha ali, a um contexto de distância.
	//
	// Pior: o teste de ida e volta passava, porque ele chamava Get com o
	// contexto especial. O teste contornava o defeito em vez de expô-lo.
	unlock func(context.Context) context.Context
}

// Open abre o cofre apontado pelas variáveis de ambiente do gopass.
//
// Não recebe caminho por parâmetro porque o gopass resolve o store por
// GOPASS_HOMEDIR, e uma segunda fonte de verdade sobre a localização é o tipo de
// coisa que diverge em silêncio. Quem define o ambiente é a unidade systemd.
func Open(ctx context.Context) (*Gopass, error) {
	store, err := api.New(ctx)
	if err != nil {
		// O texto do gopass diz "run 'gopass setup' first", conselho errado
		// nesta máquina: o droplet não tem o CLI de propósito, e o store vem
		// provisionado do Mac. Repassá-lo mandaria alguém procurar um binário
		// que não deve existir aqui — por isso a causa fica no Unwrap, e só a
		// correção que serve vai no texto.
		return nil, &opaqueError{
			summary: "cofre gopass ilegível ou ausente — reprovisione com scripts/26-setup-vault.sh",
			cause:   err,
		}
	}
	return &Gopass{store: store}, nil
}

// New monta o adapter sobre um leitor já pronto.
//
// É o construtor que o teste usa; o de produção é Open.
func New(store secretReader) *Gopass {
	return &Gopass{store: store}
}

// Get devolve o valor da chave.
//
// O valor NUNCA é registrado, nem em erro: toda mensagem daqui cita apenas o
// nome da chave. Um log de diagnóstico que imprimisse o segredo derrubaria a
// razão de o cofre existir, e logs vazam por caminhos que ninguém revisa.
func (g *Gopass) Get(ctx context.Context, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	secret, err := g.store.Get(g.withUnlock(ctx), key, "latest")
	if err != nil {
		if isNotFound(err) {
			return "", fmt.Errorf("%w: %s", ErrSecretNotFound, key)
		}
		// Só o NOME da chave vai no texto. O erro do gopass fica no Unwrap:
		// nome de chave é público, texto de terceiro não é auditável.
		return "", &opaqueError{
			summary: fmt.Sprintf("leitura de %s falhou — cofre ilegível; reprovisione com scripts/26-setup-vault.sh", key),
			cause:   err,
		}
	}
	if secret == nil {
		return "", fmt.Errorf("%w: %s", ErrSecretNotFound, key)
	}
	// Password() e não Bytes(): Bytes devolve o segredo INTEIRO, com o cabeçalho
	// GOPASS-SECRET-1.0 e os metadados. Usar Bytes mandaria isso tudo no
	// cabeçalho Authorization e a autenticação falharia por motivo obscuro.
	value := strings.TrimSpace(secret.Password())
	if value == "" {
		return "", fmt.Errorf("%w: %s está no cofre mas sem valor", ErrSecretNotFound, key)
	}
	return value, nil
}

// withUnlock devolve o contexto com o callback da senha, quando há um.
//
// Sem callback (o construtor de teste), devolve o contexto como veio.
func (g *Gopass) withUnlock(ctx context.Context) context.Context {
	if g == nil || g.unlock == nil {
		return ctx
	}
	return g.unlock(ctx)
}

// validateKey recusa nome que escaparia do diretório do store.
//
// O nome pode vir de configuração, e configuração é editável por quem tem shell
// na máquina — que inclui o próprio agente. Sem esta checagem, uma chave
// "../../../etc/shadow" transformaria o cofre em leitor de arquivo arbitrário.
func validateKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("%w: vazio", ErrInvalidKey)
	case strings.HasPrefix(key, "/"):
		return fmt.Errorf("%w: %q começa com barra", ErrInvalidKey, key)
	case strings.Contains(key, ".."):
		return fmt.Errorf("%w: %q contém ..", ErrInvalidKey, key)
	case strings.ContainsAny(key, "\x00\n\r"):
		return fmt.Errorf("%w: %q contém caractere de controle", ErrInvalidKey, key)
	}
	return nil
}

// isNotFound reconhece a ausência de chave no meio dos erros do gopass.
//
// A comparação é por texto porque o gopass devolve o erro embrulhado e sem
// sentinela exportada para este caso. É frágil de propósito na direção segura:
// se a mensagem mudar, o erro deixa de ser reconhecido como "não encontrado" e
// vira falha genérica — ruidoso, nunca silencioso.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "not found") || strings.Contains(text, "entry is not in the password store")
}
