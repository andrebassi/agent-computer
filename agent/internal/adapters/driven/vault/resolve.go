package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// SourceVault e SourceEnv nomeiam de onde o valor veio.
//
// Quem chama registra a origem no log de subida. Sem isso, uma máquina que caiu
// para o ambiente porque o cofre não abriu pareceria idêntica a uma que está
// usando o cofre — e a diferença é exatamente a que importa numa auditoria.
const (
	SourceVault = "cofre"
	SourceEnv   = "ambiente"
)

// ErrSecretUnavailable indica que nem o cofre nem o ambiente tinham o valor.
var ErrSecretUnavailable = errors.New("segredo indisponível")

// Resolve busca o segredo no cofre e, na falta dele, no ambiente.
//
// A ordem importa e a degradação é deliberada. O alvo desta mudança é o arquivo
// em claro que ficava no volume durável — é ele que entra na foto de
// `task snapshot` e vai parar na conta do DigitalOcean. A variável de ambiente
// de uma invocação por SSH nunca teve esse problema: ela morre com o processo e
// não é fotografada.
//
// Recusar o ambiente quebraria todo uso por linha de comando sem melhorar
// segurança nenhuma. Devolve a origem justamente para que a diferença apareça.
func Resolve(ctx context.Context, store SecretGetter, key, envName string) (string, string, error) {
	if store != nil {
		value, err := store.Get(ctx, key)
		if err == nil {
			return value, SourceVault, nil
		}
		// Chave ausente cai para o ambiente; cofre ilegível também, mas os dois
		// casos serão distinguidos pelo log de quem chama, que recebe a origem.
		if !errors.Is(err, ErrSecretNotFound) && !errors.Is(err, ErrInvalidKey) {
			// Erro estrutural não some: se o ambiente também não tiver o valor,
			// é esta causa que explica o porquê.
			if fallback := strings.TrimSpace(os.Getenv(envName)); fallback != "" {
				return fallback, SourceEnv, nil
			}
			return "", "", fmt.Errorf("%w: %s não veio do cofre nem de %s: %w",
				ErrSecretUnavailable, key, envName, err)
		}
	}
	if fallback := strings.TrimSpace(os.Getenv(envName)); fallback != "" {
		return fallback, SourceEnv, nil
	}
	return "", "", fmt.Errorf("%w: %s ausente no cofre e %s vazio — rode scripts/26-setup-vault.sh",
		ErrSecretUnavailable, key, envName)
}

// SecretGetter é o contrato que Resolve consome.
//
// Declarado aqui, e não importado de ports, para o pacote de adapter não
// depender do de portos só por causa de uma assinatura — a direção permitida é
// adapter para porto, e satisfazê-lo estruturalmente já basta.
type SecretGetter interface {
	Get(ctx context.Context, key string) (string, error)
}
