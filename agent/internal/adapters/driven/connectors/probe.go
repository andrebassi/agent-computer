package connectors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// probeBodyLimit corta a amostra do corpo. O diagnóstico quer saber SE
// alcançou, não o conteúdo — e um endpoint que devolve megabytes não deve
// encher o terminal de quem está diagnosticando.
const probeBodyLimit = 400

// Probe tenta alcançar uma URL pelo MESMO caminho que um conector usaria.
//
// Serve para responder, antes de cadastrar um conector, se o destino é
// alcançável — e, principalmente, para provar na máquina que um destino interno
// NÃO é. É o análogo do `-vault-check`: verificação operacional que exercita o
// caminho real em vez de reimplementá-lo.
//
// O cliente é o `newGuardedClient`, o mesmo que as ferramentas de conector
// usam. Isto não é detalhe: um diagnóstico com cliente próprio provaria que o
// diagnóstico bloqueia, não que o conector bloqueia.
//
// Só GET, e sem credencial: o objetivo é alcançabilidade. Anexar segredo aqui
// daria a quem chama um jeito de mandar credencial para onde quisesse.
func Probe(ctx context.Context, rawURL string) (string, error) {
	if err := validateBaseURL(rawURL); err != nil {
		// A checagem de cadastro roda primeiro porque é ela que recusa esquema
		// esquisito (`file://`) antes de qualquer resolução de nome.
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return "", fmt.Errorf("requisição inválida: %w", err)
	}

	response, err := newGuardedClient(httpTimeout).Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, probeBodyLimit))
	if err != nil {
		return "", fmt.Errorf("li o cabeçalho mas não o corpo: %w", err)
	}
	return fmt.Sprintf("HTTP %d\n%s", response.StatusCode, strings.TrimSpace(string(body))), nil
}
