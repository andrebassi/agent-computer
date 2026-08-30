package vault

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gopasspw/gopass/pkg/gopass"
	"github.com/gopasspw/gopass/pkg/gopass/secrets"
)

// fakeReader dubla o store do gopass sem cifrar nada em disco.
type fakeReader struct {
	values   map[string]string
	err      error
	nilValue bool
	lastName string
	lastRev  string
}

// Get devolve o segredo gravado no dublê, registrando o que foi pedido.
func (f *fakeReader) Get(_ context.Context, name, revision string) (gopass.Secret, error) {
	f.lastName, f.lastRev = name, revision
	if f.err != nil {
		return nil, f.err
	}
	if f.nilValue {
		return nil, nil
	}
	value, ok := f.values[name]
	if !ok {
		return nil, errors.New("Entry is not in the password store")
	}
	secret := secrets.NewAKV()
	secret.SetPassword(value)
	return secret, nil
}

// newFake monta um dublê com um único segredo.
func newFake(name, value string) *fakeReader {
	return &fakeReader{values: map[string]string{name: value}}
}

// O caminho feliz devolve a senha, e pede a revisão "latest".
//
// A revisão importa: sem ela o gopass devolveria erro em store com histórico, e
// o sintoma apareceria só depois de o primeiro segredo ser rotacionado.
func TestGetReturnsPasswordAtLatestRevision(t *testing.T) {
	fake := newFake("bassi/xai/apikey", "xai-chave-secreta")
	value, err := New(fake).Get(context.Background(), "bassi/xai/apikey")
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if value != "xai-chave-secreta" {
		t.Fatalf("valor errado: %q", value)
	}
	if fake.lastRev != "latest" {
		t.Fatalf("devia pedir a revisão latest, pediu %q", fake.lastRev)
	}
}

// Chave ausente vira ErrSecretNotFound, e não erro genérico.
//
// A distinção é o que separa "grave o segredo" de "reprovisione a identidade" —
// duas correções diferentes que uma mensagem única mandaria procurar errado.
func TestGetMapsMissingKeyToNotFound(t *testing.T) {
	_, err := New(newFake("outra/chave", "x")).Get(context.Background(), "bassi/xai/apikey")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("esperava ErrSecretNotFound, veio %v", err)
	}
}

// Falha de abertura NÃO pode virar "não encontrado".
//
// Canário do isNotFound: se ele passar a casar com qualquer erro, um cofre
// ilegível seria reportado como chave ausente e o diagnóstico apontaria para o
// lugar errado num incidente.
func TestGetKeepsRealFailureDistinctFromNotFound(t *testing.T) {
	fake := &fakeReader{err: errors.New("identidade age ilegível: permission denied")}
	_, err := New(fake).Get(context.Background(), "bassi/xai/apikey")
	if err == nil {
		t.Fatal("erro real devia propagar")
	}
	if errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("falha de abertura não é chave ausente: %v", err)
	}
}

// Segredo nulo sem erro não pode virar string vazia aceita.
//
// É o caso que transformaria token vazio em autenticação sem senha.
func TestGetRejectsNilSecret(t *testing.T) {
	_, err := New(&fakeReader{nilValue: true}).Get(context.Background(), "bassi/xai/apikey")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("esperava ErrSecretNotFound, veio %v", err)
	}
}

// Segredo presente mas com senha em branco também é recusado.
func TestGetRejectsBlankPassword(t *testing.T) {
	_, err := New(newFake("bassi/xai/apikey", "   \n ")).Get(context.Background(), "bassi/xai/apikey")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("esperava ErrSecretNotFound, veio %v", err)
	}
}

// A chave é validada ANTES de tocar no cofre.
//
// Canário de travessia: retirar validateKey faz este teste reprovar, porque o
// dublê registraria a tentativa. Sem ele, uma chave vinda de configuração
// editável por quem tem shell viraria leitor de arquivo arbitrário.
func TestGetRejectsTraversalBeforeTouchingStore(t *testing.T) {
	hostile := []string{
		"../../../etc/shadow",
		"/etc/shadow",
		"",
		"chave\ncom-quebra",
		"chave\x00nula",
	}
	for _, key := range hostile {
		fake := newFake("bassi/xai/apikey", "valor")
		_, err := New(fake).Get(context.Background(), key)
		if !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("%q devia ser recusada com ErrInvalidKey, veio %v", key, err)
		}
		if fake.lastName != "" {
			t.Fatalf("%q chegou ao cofre como %q — a validação veio tarde demais", key, fake.lastName)
		}
	}
}

// Nenhuma mensagem de erro pode conter o segredo.
//
// Teste genérico de propósito: varre as mensagens em vez de conferir uma a uma,
// para pegar também a mensagem que alguém acrescentar depois e esquecer de
// higienizar. Log vaza por caminho que ninguém revisa.
func TestErrorsNeverLeakTheSecretValue(t *testing.T) {
	const secretValue = "valor-ultrassecreto-que-nao-pode-vazar"
	cases := map[string]*fakeReader{
		"erro do cofre": {values: map[string]string{"k": secretValue}, err: errors.New("falha: " + secretValue)},
		"chave ausente": newFake("outra", secretValue),
	}
	for name, fake := range cases {
		_, err := New(fake).Get(context.Background(), "k")
		if err == nil {
			continue
		}
		if strings.Contains(err.Error(), secretValue) {
			t.Fatalf("%s: a mensagem carregou o segredo: %q", name, err)
		}
	}
}

// Open recusa quando não há cofre, e a mensagem aponta o script certo.
//
// O gopass diz "run 'gopass setup' first", conselho errado nesta máquina: o
// droplet não tem o CLI de propósito. Sem a tradução, alguém procuraria um
// binário que não deve existir ali.
func TestOpenFailsWithActionableMessageWhenStoreMissing(t *testing.T) {
	t.Setenv("GOPASS_HOMEDIR", t.TempDir())
	t.Setenv("GOPASS_CONFIG_NOSYSTEM", "true")
	_, err := Open(context.Background())
	if err == nil {
		t.Fatal("cofre inexistente devia recusar")
	}
	if !strings.Contains(err.Error(), "26-setup-vault.sh") {
		t.Fatalf("a mensagem devia apontar o script de reprovisionamento: %v", err)
	}
	if strings.Contains(err.Error(), "gopass setup") {
		t.Fatalf("a mensagem não devia repassar o conselho do gopass: %v", err)
	}
}
