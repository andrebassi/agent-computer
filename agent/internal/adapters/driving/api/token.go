package api

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// minTokenLength é o tamanho mínimo aceito.
//
// 32 caracteres é o que `openssl rand -hex 16` produz. Abaixo disso o token
// costuma ser um valor digitado à mão — e valor digitado à mão é adivinhável.
const minTokenLength = 32

// Erros de leitura do token. São distintos porque cada um pede uma correção
// diferente, e uma mensagem genérica faria procurar no lugar errado.
var (
	ErrTokenMissing = errors.New("arquivo de token ausente")
	ErrTokenLoose   = errors.New("arquivo de token legível por outros")
	ErrTokenShort   = errors.New("token curto demais")
)

// ReadToken lê o segredo do arquivo e RECUSA se a permissão for frouxa.
//
// Recusar é o ponto. Uma porta que sobe sem autenticação porque o arquivo sumiu
// é o pior desfecho possível: tudo funciona, ninguém percebe, e a máquina fica
// aberta com acesso a shell, navegador e credenciais de conta.
//
// Falha FECHADA, sempre, e a mensagem diz como consertar — descobrir isso no
// meio de um incidente é tarde demais.
func ReadToken(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: nenhum caminho configurado", ErrTokenMissing)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s — gere com scripts/23-setup-api-token.sh", ErrTokenMissing, path)
		}
		return "", err
	}
	// O arquivo carrega uma credencial que dá acesso total ao computador. Este
	// computador é compartilhado por todos os agentes da conta, então "legível
	// por outros" aqui significa legível por qualquer processo da máquina.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("%w: %s tem permissão %o — corrija com chmod 600", ErrTokenLoose, path, perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// TrimSpace porque um editor acrescenta quebra de linha no fim, e um token
	// com "\n" nunca casa com o que o cliente envia — falha que parece token
	// errado quando é só o arquivo.
	token := strings.TrimSpace(string(data))
	if len(token) < minTokenLength {
		return "", fmt.Errorf("%w: %d caracteres, mínimo %d", ErrTokenShort, len(token), minTokenLength)
	}
	return token, nil
}
