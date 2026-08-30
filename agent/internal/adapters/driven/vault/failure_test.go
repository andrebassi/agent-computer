package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cada campo obrigatório ausente é recusado por Validate, com motivo próprio.
//
// Tabela em vez de um teste por campo porque a lista vai crescer, e um caso novo
// deve custar uma linha — não um teste inteiro que alguém decide não escrever.
func TestValidateRejectsEachMissingField(t *testing.T) {
	full := Config{StoreDir: "/a", HomeDir: "/b", Passphrase: strings.Repeat("x", 30)}
	cases := map[string]Config{
		"sem diretório do cofre":      {HomeDir: full.HomeDir, Passphrase: full.Passphrase},
		"sem diretório da identidade": {StoreDir: full.StoreDir, Passphrase: full.Passphrase},
		"sem senha":                   {StoreDir: full.StoreDir, HomeDir: full.HomeDir},
	}
	for name, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s: devia ser recusado", name)
		}
	}
	if err := full.Validate(); err != nil {
		t.Fatalf("configuração completa devia passar: %v", err)
	}
}

// OpenWith e Put recusam configuração inválida ANTES de tocar em disco.
//
// Sem isto, uma configuração pela metade criaria diretórios no lugar errado e o
// erro só apareceria depois, com lixo já gravado.
func TestOpenAndPutValidateBeforeTouchingDisk(t *testing.T) {
	broken := Config{StoreDir: "", HomeDir: "/b", Passphrase: strings.Repeat("x", 30)}
	if _, err := OpenWith(context.Background(), broken); err == nil {
		t.Fatal("OpenWith devia recusar configuração inválida")
	}
	if err := Put(context.Background(), broken, "chave", "valor"); err == nil {
		t.Fatal("Put devia recusar configuração inválida")
	}
}

// Put recusa nome de chave que escaparia do store.
//
// A validação precisa vir antes de abrir o cofre: abrir primeiro gastaria a
// derivação scrypt para depois recusar, e num laço isso é negação de serviço.
func TestPutRejectsTraversalKey(t *testing.T) {
	cfg := testConfig(t)
	if err := Put(context.Background(), cfg, "../../etc/shadow", "valor"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("esperava ErrInvalidKey, veio %v", err)
	}
}

// Disco sem permissão de escrita faz a criação falhar com erro, não com pânico.
//
// O caso acontece de verdade: /workspace montado somente leitura depois de o
// volume falhar é exatamente o estado em que o serviço tenta subir.
func TestCreateFailsCleanlyOnReadOnlyParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("como root a permissão do diretório não impede a escrita")
	}
	base := t.TempDir()
	locked := filepath.Join(base, "sem-escrita")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("preparo falhou: %v", err)
	}
	// Devolve a permissão para o TempDir conseguir se limpar.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	cfg := Config{
		StoreDir:   filepath.Join(locked, "vault"),
		HomeDir:    filepath.Join(base, "home"),
		Passphrase: strings.Repeat("x", 30),
	}
	err := Create(context.Background(), cfg)
	if err == nil {
		t.Fatal("criação em diretório sem escrita devia falhar")
	}
	if !strings.Contains(err.Error(), "diretório do cofre") {
		t.Fatalf("a mensagem devia apontar o diretório do cofre: %v", err)
	}
}

// Cofre inexistente faz OpenWith recusar com a correção no texto.
func TestOpenWithFailsWhenVaultWasNeverCreated(t *testing.T) {
	cfg := testConfig(t)
	if _, err := OpenWith(context.Background(), cfg); err == nil {
		t.Fatal("abrir cofre inexistente devia falhar")
	}
}

// isNotFound distingue ausência de chave de qualquer outro erro.
//
// Tabela porque é comparação por texto — frágil de propósito na direção segura,
// mas que precisa dos dois sentidos travados para não virar "casa com tudo".
func TestIsNotFoundRecognizesOnlyAbsence(t *testing.T) {
	absent := []error{
		errors.New("Entry is not in the password store"),
		errors.New("secret not found"),
		errors.New("NOT FOUND"),
	}
	for _, err := range absent {
		if !isNotFound(err) {
			t.Fatalf("devia reconhecer como ausência: %v", err)
		}
	}
	other := []error{
		nil,
		errors.New("identidade age ilegível"),
		errors.New("permission denied"),
	}
	for _, err := range other {
		if isNotFound(err) {
			t.Fatalf("não devia tratar como ausência: %v", err)
		}
	}
}

// Put num cofre inexistente falha na abertura, sem gravar nada.
//
// É o caminho real de uma máquina cujo volume não montou: o provisionamento
// precisa parar com erro claro, não criar um cofre paralelo no lugar errado.
func TestPutFailsWhenVaultWasNeverCreated(t *testing.T) {
	cfg := testConfig(t)
	if err := Put(context.Background(), cfg, "bassi/xai/apikey", "valor"); err == nil {
		t.Fatal("gravar em cofre inexistente devia falhar")
	}
}

// Identidade em diretório sem escrita faz a criação falhar antes dos destinatários.
//
// A ordem importa: se os destinatários fossem gravados primeiro, sobraria um
// store marcado como inicializado sem identidade que o abra — cofre que parece
// pronto e não é.
func TestCreateFailsWhenIdentityDirectoryIsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("como root a permissão do diretório não impede a escrita")
	}
	base := t.TempDir()
	locked := filepath.Join(base, "home-sem-escrita")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("preparo falhou: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	cfg := Config{
		StoreDir:   filepath.Join(base, "vault"),
		HomeDir:    filepath.Join(locked, "home"),
		Passphrase: strings.Repeat("x", 30),
	}
	if err := Create(context.Background(), cfg); err == nil {
		t.Fatal("criação devia falhar")
	}
	if _, err := os.Stat(filepath.Join(cfg.StoreDir, recipientsFile)); err == nil {
		t.Fatal("os destinatários foram gravados apesar de a identidade ter falhado")
	}
}

// opaqueError esconde a causa do texto mas a mantém alcançável.
//
// As duas metades importam: o texto vai para log, a causa vai para o depurador.
func TestOpaqueErrorHidesCauseFromTextButKeepsItReachable(t *testing.T) {
	cause := errors.New("segredo-no-texto-do-erro")
	err := &opaqueError{summary: "falhou", cause: cause}
	if strings.Contains(err.Error(), "segredo-no-texto-do-erro") {
		t.Fatalf("a causa vazou para o texto: %q", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("a causa devia continuar alcançável por errors.Is")
	}
}
