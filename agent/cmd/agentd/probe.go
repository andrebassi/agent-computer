package main

import (
	"context"
	"fmt"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/connectors"
)

// runConnectorProbe tenta alcançar uma URL pelo caminho de um conector.
//
// Devolve erro quando o destino é recusado — e é assim que o teste de máquina
// distingue bloqueio de falha de rede: o texto do erro diz qual foi o caso.
//
// Não aceita credencial nem método além de GET. A sonda existe para responder
// "isto é alcançável?", e qualquer coisa além disso viraria um jeito de mandar
// requisição arbitrária a partir do processo que tem o cofre aberto.
func runConnectorProbe(ctx context.Context, rawURL string) error {
	resposta, err := connectors.Probe(ctx, rawURL)
	if err != nil {
		return fmt.Errorf("não alcancei %s: %w", rawURL, err)
	}
	fmt.Println(resposta)
	return nil
}
