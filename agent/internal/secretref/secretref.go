// Package secretref resolve uma credencial pelo cofre e, na falta dele, por arquivo.
//
// Existe porque a regra passou a ser "todo recurso pega seus dados do cofre", e
// os consumidores estão espalhados: chave do modelo, token da porta HTTP,
// credencial de conector, credencial do agente de código. Sem um ponto único, a
// ordem de busca seria reescrita em cada um — e divergiria, que é exatamente
// como um deles continuaria lendo texto em claro sem ninguém notar.
//
// Fica fora de adapters/ de propósito: adapter não importa adapter, e tanto o
// pacote de conectores quanto o de ferramentas precisam disto.
package secretref

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// SecretGetter é o cofre visto por quem só precisa ler.
//
// Declarado aqui, e não importado de ports, para este pacote não puxar o
// domínio junto: ele é folha de propósito, e folha sem dependência é o que
// permite qualquer adapter importá-lo sem criar ciclo.
type SecretGetter interface {
	Get(ctx context.Context, key string) (string, error)
}

// Origens possíveis de uma credencial, ditas em voz alta por quem resolve.
//
// A distinção existe para a operação: uma máquina que caiu para o arquivo
// parece idêntica a uma que usa o cofre, e é essa diferença que uma auditoria
// procura.
const (
	SourceVault = "cofre"
	SourceFile  = "arquivo"
)

// ErrUnavailable indica credencial ausente nas duas origens.
var ErrUnavailable = errors.New("credencial indisponível")

// Resolver busca credenciais, preferindo sempre o cofre.
type Resolver struct {
	store SecretGetter
}

// New monta o resolvedor. Um cofre nulo é aceito: a máquina ainda não
// provisionada continua funcionando pelo arquivo, e a migração não exige corte
// de serviço.
func New(store SecretGetter) *Resolver {
	return &Resolver{store: store}
}

// Value devolve a credencial e de onde ela veio.
//
// O cofre VENCE quando os dois têm valor. Se o arquivo ganhasse, provisionar o
// cofre não mudaria nada e a migração seria só aparência.
//
// O valor NUNCA entra em mensagem de erro — só a chave e o caminho, que são
// públicos. Erro vai para log, e log vaza por caminho que ninguém revisa.
func (r *Resolver) Value(ctx context.Context, key, filePath string) (string, string, error) {
	if r != nil && r.store != nil && key != "" {
		if value, err := r.store.Get(ctx, key); err == nil {
			if value = strings.TrimSpace(value); value != "" {
				return value, SourceVault, nil
			}
		}
	}
	if filePath == "" {
		return "", "", fmt.Errorf("%w: %s ausente no cofre e sem arquivo alternativo", ErrUnavailable, key)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s ausente no cofre e %s ilegível", ErrUnavailable, key, filePath)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", "", fmt.Errorf("%w: %s ausente no cofre e %s vazio", ErrUnavailable, key, filePath)
	}
	return value, SourceFile, nil
}
