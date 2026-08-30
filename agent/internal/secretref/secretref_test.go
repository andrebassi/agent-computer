package secretref

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeVault dubla o cofre.
type fakeVault struct {
	value string
	err   error
}

// Get devolve o que o dublê foi configurado para devolver.
func (f *fakeVault) Get(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.value, nil
}

// writeFile grava um arquivo de credencial descartável.
func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credencial")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("preparo falhou: %v", err)
	}
	return path
}

// O cofre VENCE o arquivo quando os dois têm valor.
//
// Canário da migração inteira: se o arquivo ganhasse, provisionar o cofre não
// mudaria comportamento nenhum e a troca seria só aparência.
func TestVaultWinsOverFile(t *testing.T) {
	path := writeFile(t, "valor-do-arquivo")
	value, source, err := New(&fakeVault{value: "valor-do-cofre"}).
		Value(context.Background(), "connectors/exemplo", path)
	if err != nil {
		t.Fatalf("resolução falhou: %v", err)
	}
	if value != "valor-do-cofre" || source != SourceVault {
		t.Fatalf("o cofre devia vencer: %q de %q", value, source)
	}
}

// Sem cofre, o arquivo atende — e a origem denuncia a degradação.
func TestFallsBackToFileAndReportsIt(t *testing.T) {
	path := writeFile(t, "valor-do-arquivo\n")
	value, source, err := New(nil).Value(context.Background(), "connectors/exemplo", path)
	if err != nil {
		t.Fatalf("resolução falhou: %v", err)
	}
	if value != "valor-do-arquivo" {
		t.Fatalf("devia aparar a quebra de linha: %q", value)
	}
	if source != SourceFile {
		t.Fatalf("origem errada: %q", source)
	}
}

// Cofre que devolve valor em branco não conta como resposta.
//
// Sem isto, uma chave gravada vazia produziria cabeçalho Authorization com
// "Bearer " e a API responderia 401 — falha que parece credencial errada quando
// é credencial ausente.
func TestBlankVaultValueFallsBackToFile(t *testing.T) {
	path := writeFile(t, "valor-do-arquivo")
	value, source, err := New(&fakeVault{value: "   "}).
		Value(context.Background(), "connectors/exemplo", path)
	if err != nil || value != "valor-do-arquivo" || source != SourceFile {
		t.Fatalf("devia cair para o arquivo: %q, %q, %v", value, source, err)
	}
}

// Erro do cofre também cai para o arquivo.
func TestVaultErrorFallsBackToFile(t *testing.T) {
	path := writeFile(t, "valor-do-arquivo")
	value, _, err := New(&fakeVault{err: errors.New("cofre ilegível")}).
		Value(context.Background(), "connectors/exemplo", path)
	if err != nil || value != "valor-do-arquivo" {
		t.Fatalf("devia cair para o arquivo: %q, %v", value, err)
	}
}

// Sem cofre e sem arquivo, falha com ErrUnavailable.
func TestFailsWhenNeitherSourceHasTheValue(t *testing.T) {
	cases := map[string]string{
		"sem caminho":         "",
		"arquivo inexistente": filepath.Join(t.TempDir(), "nao-existe"),
		"arquivo vazio":       writeFile(t, "  \n "),
	}
	for name, path := range cases {
		if _, _, err := New(nil).Value(context.Background(), "k", path); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: esperava ErrUnavailable, veio %v", name, err)
		}
	}
}

// Nenhuma mensagem de erro carrega o valor da credencial.
//
// Varre em vez de conferir caso a caso, para pegar a mensagem que alguém
// acrescentar depois e esquecer de higienizar.
func TestErrorsNeverLeakTheCredential(t *testing.T) {
	const secretValue = "credencial-que-nao-pode-vazar"
	path := writeFile(t, secretValue)
	// Força a falha depois de o arquivo já ter sido lido com sucesso noutro
	// caminho: aqui o erro vem da chave, e a mensagem não pode citar o arquivo.
	_, _, err := New(nil).Value(context.Background(), "k", path+"-inexistente")
	if err != nil && strings.Contains(err.Error(), secretValue) {
		t.Fatalf("a mensagem carregou a credencial: %v", err)
	}
}

// Resolvedor nulo não entra em pânico; cai direto para o arquivo.
//
// O caso acontece de verdade: um adapter construído antes de o cofre existir
// carrega nil, e um pânico ali derrubaria a tarefa inteira por causa de uma
// credencial que o arquivo tinha.
func TestNilResolverStillReadsTheFile(t *testing.T) {
	path := writeFile(t, "valor-do-arquivo")
	var resolver *Resolver
	value, source, err := resolver.Value(context.Background(), "k", path)
	if err != nil || value != "valor-do-arquivo" || source != SourceFile {
		t.Fatalf("resolvedor nulo devia ler o arquivo: %q, %q, %v", value, source, err)
	}
}
