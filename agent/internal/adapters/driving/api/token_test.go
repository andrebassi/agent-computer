package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeToken grava um arquivo de token com a permissão pedida.
func writeToken(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	// WriteFile respeita o umask, então a permissão é forçada depois.
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	return path
}

// Token válido é lido, sem a quebra de linha do editor.
//
// Um token com "\n" no fim nunca casa com o que o cliente envia — e a falha
// parece token errado quando é só o arquivo.
func TestReadTokenTrimsTrailingNewline(t *testing.T) {
	path := writeToken(t, testToken+"\n", 0o600)
	got, err := ReadToken(path)
	if err != nil {
		t.Fatalf("ReadToken falhou: %v", err)
	}
	if got != testToken {
		t.Fatalf("esperava %q, veio %q", testToken, got)
	}
}

// Falha FECHADA em todos os casos ruins.
//
// Uma porta que sobe sem autenticação porque o arquivo sumiu é o pior desfecho
// possível: tudo funciona, ninguém percebe, e a máquina fica aberta com acesso a
// shell, navegador e credenciais.
func TestReadTokenFailsClosed(t *testing.T) {
	cases := []struct {
		nome     string
		prepara  func(*testing.T) string
		esperado error
	}{
		{
			nome:     "caminho vazio",
			prepara:  func(*testing.T) string { return "" },
			esperado: ErrTokenMissing,
		},
		{
			nome:     "arquivo ausente",
			prepara:  func(t *testing.T) string { return filepath.Join(t.TempDir(), "sumido") },
			esperado: ErrTokenMissing,
		},
		{
			nome:     "legível por outros",
			prepara:  func(t *testing.T) string { return writeToken(t, testToken, 0o644) },
			esperado: ErrTokenLoose,
		},
		{
			nome:     "legível pelo grupo",
			prepara:  func(t *testing.T) string { return writeToken(t, testToken, 0o640) },
			esperado: ErrTokenLoose,
		},
		{
			nome:     "curto demais",
			prepara:  func(t *testing.T) string { return writeToken(t, "curto", 0o600) },
			esperado: ErrTokenShort,
		},
		{
			nome:     "vazio",
			prepara:  func(t *testing.T) string { return writeToken(t, "", 0o600) },
			esperado: ErrTokenShort,
		},
	}
	for _, c := range cases {
		t.Run(c.nome, func(t *testing.T) {
			_, err := ReadToken(c.prepara(t))
			if !errors.Is(err, c.esperado) {
				t.Fatalf("esperava %v, veio %v", c.esperado, err)
			}
		})
	}
}

// A mensagem de erro diz COMO consertar.
//
// Descobrir o procedimento no meio de um incidente é tarde demais.
func TestReadTokenErrorsSayHowToFix(t *testing.T) {
	_, err := ReadToken(writeToken(t, testToken, 0o644))
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a mensagem devia dizer como corrigir: %v", err)
	}

	_, err = ReadToken(filepath.Join(t.TempDir(), "sumido"))
	if !strings.Contains(err.Error(), "scripts/") {
		t.Fatalf("a mensagem devia apontar o script de geração: %v", err)
	}
}
