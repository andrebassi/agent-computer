package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testConfig monta um cofre descartável com identidade e store separados.
//
// A separação espelha a de produção — store no volume durável, identidade no
// disco do sistema — porque é ela que Validate exige.
func testConfig(t *testing.T) Config {
	t.Helper()
	base := t.TempDir()
	return Config{
		StoreDir:   filepath.Join(base, "vault"),
		HomeDir:    filepath.Join(base, "home"),
		Passphrase: "senha-longa-o-suficiente-para-o-piso",
	}
}

// O cofre criado em Go é legível pelo gopass, e o valor volta inteiro.
//
// É O teste do pacote. Prova que criar o store sem o CLI produz um formato que a
// biblioteca aceita — a aposta inteira do desenho. Se ele reprovar, o cofre não
// serve, por mais que os testes de unidade passem.
func TestCreateWriteAndReadRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	if err := Create(ctx, cfg); err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	const key, value = "bassi/xai/apikey", "xai-chave-de-teste-1234567890"
	if err := Put(ctx, cfg, key, value); err != nil {
		t.Fatalf("gravação falhou: %v", err)
	}
	store, err := OpenWith(ctx, cfg)
	if err != nil {
		t.Fatalf("abertura falhou: %v", err)
	}
	got, err := store.Get(cfg.withPassphrase(ctx), key)
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if got != value {
		t.Fatalf("valor não voltou inteiro: %q", got)
	}
}

// O segredo fica CIFRADO em disco — é a razão de o cofre existir.
//
// Canário do ganho declarado: o volume é fotografado por `task snapshot` e a
// foto vai para a conta do DigitalOcean. Se o valor aparecesse em claro nos
// arquivos do store, a troca não teria comprado nada.
func TestStoredSecretIsNotReadableInPlaintext(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	if err := Create(ctx, cfg); err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	const value = "valor-que-nao-pode-aparecer-em-claro"
	if err := Put(ctx, cfg, "bassi/xai/apikey", value); err != nil {
		t.Fatalf("gravação falhou: %v", err)
	}
	found := false
	err := filepath.Walk(cfg.StoreDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		found = true
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), value) {
			return errors.New("segredo em claro em " + path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("varredura do store: %v", err)
	}
	if !found {
		t.Fatal("o store ficou vazio — a varredura não provou nada")
	}
}

// A identidade age também é cifrada, e não fica no diretório do cofre.
//
// Guardar as duas na mesma partição tornaria a foto do volume autossuficiente e
// o ganho seria zero — é a invariante que a separação existe para manter.
func TestIdentityIsSealedAndLivesOutsideTheStore(t *testing.T) {
	cfg := testConfig(t)
	if err := Create(context.Background(), cfg); err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	data, err := os.ReadFile(cfg.identityPath())
	if err != nil {
		t.Fatalf("identidade ilegível: %v", err)
	}
	if strings.Contains(string(data), "AGE-SECRET-KEY") {
		t.Fatal("a chave privada está em claro no arquivo de identidade")
	}
	if strings.HasPrefix(cfg.identityPath(), cfg.StoreDir) {
		t.Fatalf("a identidade caiu dentro do cofre: %s", cfg.identityPath())
	}
	info, err := os.Stat(cfg.identityPath())
	if err != nil {
		t.Fatalf("stat falhou: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("identidade legível por outros: %o", perm)
	}
}

// Criar em cima de cofre existente é RECUSADO.
//
// Sobrescrever geraria identidade nova e órfãos todos os segredos já gravados,
// que continuam no disco e param de decifrar — falha que só aparece na próxima
// leitura, longe da causa.
func TestCreateRefusesToOverwriteExistingVault(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	if err := Create(ctx, cfg); err != nil {
		t.Fatalf("primeira criação falhou: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(cfg.StoreDir, recipientsFile))
	if err != nil {
		t.Fatalf("leitura dos destinatários falhou: %v", err)
	}
	if err := Create(ctx, cfg); !errors.Is(err, ErrVaultExists) {
		t.Fatalf("esperava ErrVaultExists, veio %v", err)
	}
	after, err := os.ReadFile(filepath.Join(cfg.StoreDir, recipientsFile))
	if err != nil {
		t.Fatalf("releitura falhou: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("os destinatários foram trocados apesar da recusa")
	}
}

// Senha errada não abre o cofre.
//
// Canário da cifra: sem ele, um erro que fizesse a identidade ser gravada em
// claro passaria despercebido — tudo continuaria funcionando.
func TestWrongPassphraseCannotReadTheSecret(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	if err := Create(ctx, cfg); err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := Put(ctx, cfg, "bassi/xai/apikey", "valor-secreto"); err != nil {
		t.Fatalf("gravação falhou: %v", err)
	}
	wrong := cfg
	wrong.Passphrase = "senha-errada-mas-longa-o-bastante"
	store, err := OpenWith(ctx, wrong)
	if err != nil {
		return // recusar já na abertura também satisfaz
	}
	if _, err := store.Get(wrong.withPassphrase(ctx), "bassi/xai/apikey"); err == nil {
		t.Fatal("a senha errada leu o segredo")
	}
}

// Configuração que juntaria cofre e identidade é recusada.
func TestValidateRejectsSameDirectoryForStoreAndIdentity(t *testing.T) {
	cfg := Config{StoreDir: "/x", HomeDir: "/x", Passphrase: strings.Repeat("a", 30)}
	if err := cfg.Validate(); err == nil {
		t.Fatal("mesmo diretório para cofre e identidade devia ser recusado")
	}
}

// Senha curta demais é recusada antes de qualquer escrita.
//
// O arquivo cifrado fica num disco que pode ser copiado, e scrypt offline aceita
// quantas tentativas o atacante quiser: senha curta é a única barreira depois do
// vazamento.
func TestValidateRejectsShortPassphrase(t *testing.T) {
	cfg := testConfig(t)
	cfg.Passphrase = "curta"
	if err := Create(context.Background(), cfg); err == nil {
		t.Fatal("senha curta devia ser recusada")
	}
	if _, err := os.Stat(cfg.StoreDir); err == nil {
		t.Fatal("nada devia ter sido criado em disco")
	}
}
