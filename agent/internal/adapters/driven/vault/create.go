package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
	"github.com/gopasspw/gopass/pkg/ctxutil"
	"github.com/gopasspw/gopass/pkg/gopass"
	"github.com/gopasspw/gopass/pkg/gopass/secrets"
)

// recipientsFile é o arquivo que marca um store como inicializado.
//
// O nome vem do gopass (age.IDFile) e é a ÚNICA condição que ele checa: existir
// este arquivo no diretório do store basta para IsInitialized devolver true e
// para o backend age ser escolhido na detecção. É por isso que criar o cofre em
// Go é possível sem o CLI.
const recipientsFile = ".age-recipients"

// ErrVaultExists indica que já há um cofre no lugar.
//
// Sobrescrever seria destrutivo e silencioso: uma identidade nova órfã TODOS os
// segredos já gravados, que continuam no disco e param de decifrar. O sintoma
// apareceria só na próxima leitura, longe da causa.
var ErrVaultExists = errors.New("já existe cofre neste diretório")

// Config diz onde o cofre mora e como a identidade é protegida.
type Config struct {
	// StoreDir guarda os segredos cifrados. Vai no volume durável.
	StoreDir string
	// HomeDir guarda a identidade age. Vai no disco do SISTEMA, e é isso que
	// faz a foto do volume ser inútil sozinha.
	HomeDir string
	// Passphrase cifra o arquivo de identidade, com scrypt.
	Passphrase string
}

// identityPath devolve o caminho do arquivo de identidade age.
//
// O layout é ditado pelo gopass (appdir.UserConfig() + "age/identities") e não
// pode ser escolhido: é onde ele vai procurar.
func (c Config) identityPath() string {
	return filepath.Join(c.HomeDir, ".config", "gopass", "age", "identities")
}

// apply publica a configuração no ambiente do processo.
//
// O gopass resolve store e identidade por variável de ambiente, sem API para
// injetá-las. Mexer no ambiente do processo é global e por isso só se faz na
// composição, antes de qualquer goroutine — nunca no meio de uma requisição.
//
// GOPASS_CONFIG_NOSYSTEM impede que um config.yml do usuário da máquina mude o
// store por baixo: numa máquina de agente isso apontaria o cofre para o lugar
// errado sem nenhum aviso.
func (c Config) apply() error {
	for name, value := range map[string]string{
		"GOPASS_HOMEDIR":         c.HomeDir,
		"PASSWORD_STORE_DIR":     c.StoreDir,
		"GOPASS_CONFIG_NOSYSTEM": "true",
	} {
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("não consegui definir %s: %w", name, err)
		}
	}
	return nil
}

// withPassphrase liga o callback que decifra o arquivo de identidade.
//
// O gopass lê GOPASS_AGE_PASSWORD no main.go do CLI, que não roda aqui — pela
// biblioteca, sem este callback ele tentaria abrir um pinentry interativo e o
// serviço travaria na subida, sem log, esperando alguém digitar.
func (c Config) withPassphrase(ctx context.Context) context.Context {
	passphrase := []byte(c.Passphrase)
	return ctxutil.WithPasswordCallback(ctx, func(string, bool) ([]byte, error) {
		return passphrase, nil
	})
}

// Validate confere que a configuração descreve um cofre utilizável.
func (c Config) Validate() error {
	switch {
	case c.StoreDir == "":
		return errors.New("diretório do cofre não informado")
	case c.HomeDir == "":
		return errors.New("diretório da identidade não informado")
	case c.StoreDir == c.HomeDir:
		return errors.New("cofre e identidade no mesmo diretório: a foto do volume levaria as duas e o ganho seria zero")
	case len(c.Passphrase) < minPassphraseLength:
		return fmt.Errorf("senha do cofre com %d caracteres, mínimo %d", len(c.Passphrase), minPassphraseLength)
	}
	return nil
}

// minPassphraseLength é o piso da senha que protege a identidade.
//
// 24 caracteres porque o arquivo cifrado fica num disco que pode ser copiado —
// e scrypt offline aceita quantas tentativas o atacante quiser. Senha curta aqui
// não é inconveniente, é a única barreira depois de o arquivo vazar.
const minPassphraseLength = 24

// Create monta um cofre novo: identidade age, arquivo de destinatários e diretórios.
//
// Feito em Go, e não em shell, de propósito: o mesmo binário que lê o cofre é o
// que o cria, então não há como o formato divergir entre quem escreve e quem lê.
func Create(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(cfg.StoreDir, recipientsFile)); err == nil {
		return fmt.Errorf("%w: %s", ErrVaultExists, cfg.StoreDir)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return fmt.Errorf("geração da identidade age falhou: %w", err)
	}
	// 0700 nos dois: o modo do diretório é o que impede outro usuário de listar
	// os nomes das chaves, que já entregam metade da informação.
	if err := os.MkdirAll(cfg.StoreDir, 0o700); err != nil {
		return fmt.Errorf("criação do diretório do cofre falhou: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.identityPath()), 0o700); err != nil {
		return fmt.Errorf("criação do diretório da identidade falhou: %w", err)
	}
	// A identidade é gravada ANTES dos destinatários. Se a ordem se invertesse e
	// a segunda escrita falhasse, sobraria um store marcado como inicializado
	// sem nenhuma identidade capaz de abri-lo — cofre que parece pronto e não é.
	sealed, err := sealIdentity(identity.String(), cfg.Passphrase)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfg.identityPath(), sealed, 0o600); err != nil {
		return fmt.Errorf("gravação da identidade falhou: %w", err)
	}
	recipients := identity.Recipient().String() + "\n"
	if err := os.WriteFile(filepath.Join(cfg.StoreDir, recipientsFile), []byte(recipients), 0o600); err != nil {
		return fmt.Errorf("gravação dos destinatários falhou: %w", err)
	}
	return nil
}

// sealIdentity cifra a chave privada age com a senha, usando scrypt.
//
// Sem armor: o gopass lê o arquivo com age.Decrypt direto sobre os bytes crus, e
// a versão em base64 do armor faria a decifragem falhar com erro de formato.
func sealIdentity(privateKey, passphrase string) ([]byte, error) {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("preparo da cifra por senha falhou: %w", err)
	}
	buffer := &bytes.Buffer{}
	writer, err := age.Encrypt(buffer, recipient)
	if err != nil {
		return nil, fmt.Errorf("abertura da cifra falhou: %w", err)
	}
	if _, err := io.WriteString(writer, privateKey+"\n"); err != nil {
		return nil, fmt.Errorf("escrita da identidade cifrada falhou: %w", err)
	}
	// Close é o que grava o rodapé de autenticação. Sem ele o arquivo sai
	// truncado e só falha na leitura, longe daqui.
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("fechamento da cifra falhou: %w", err)
	}
	return buffer.Bytes(), nil
}

// OpenWith abre o cofre descrito pela configuração.
//
// É o construtor de produção: aplica o ambiente, liga o callback da senha e só
// então pede o store ao gopass.
func OpenWith(ctx context.Context, cfg Config) (*Gopass, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.apply(); err != nil {
		return nil, err
	}
	store, err := Open(cfg.withPassphrase(ctx))
	if err != nil {
		return nil, err
	}
	// O adapter passa a carregar a senha: a decifragem acontece a cada leitura,
	// não ao abrir, e quem chama Get não tem como saber disso.
	store.unlock = cfg.withPassphrase
	return store, nil
}

// Put grava um segredo no cofre.
//
// Existe para o provisionamento rodar pelo mesmo binário que lê: um gravador
// escrito noutra linguagem poderia produzir um formato que só falha na leitura.
func Put(ctx context.Context, cfg Config, key, value string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	store, err := OpenWith(ctx, cfg)
	if err != nil {
		return err
	}
	writer, ok := store.store.(secretWriter)
	if !ok {
		return errors.New("cofre aberto em modo somente leitura")
	}
	secret := secrets.NewAKV()
	secret.SetPassword(value)
	if err := writer.Set(cfg.withPassphrase(ctx), key, secret); err != nil {
		// O valor NUNCA entra na mensagem — só o nome da chave.
		return &opaqueError{
			summary: fmt.Sprintf("gravação de %s no cofre falhou", key),
			cause:   err,
		}
	}
	return nil
}

// secretWriter é a fatia de escrita do gopass.
type secretWriter interface {
	Set(ctx context.Context, name string, sec gopass.Byter) error
}
