package main

import (
	"context"
	"fmt"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/events"
)

// runDrain entrega os avisos enfileirados.
//
// Roda em PROCESSO SEPARADO, chamado por timer, e é isso que satisfaz o
// requisito duro da proatividade: a entrega não depende da conexão que iniciou a
// tarefa. Matar a sessão SSH que disparou o trabalho não mata o aviso, porque o
// aviso nunca esteve nela — está num arquivo no volume durável.
//
// Sem `-webhook`, apenas LISTA o que está pendente e não consome a fila. É o
// primeiro comando a rodar quando alguém desconfia que um aviso se perdeu, e
// consumir a fila nesse momento destruiria justamente a evidência.
func runDrain(ctx context.Context, stateDir, webhookURL string) error {
	spool, err := events.NewSpool(stateDir + "/events")
	if err != nil {
		return err
	}

	if webhookURL == "" {
		pending, err := spool.Pending(ctx)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			fmt.Println("nenhum aviso pendente")
			return nil
		}
		fmt.Printf("%d aviso(s) pendente(s), sem destino configurado:\n", len(pending))
		for _, line := range events.Describe(pending) {
			fmt.Println("  " + line)
		}
		fmt.Println("\nuse -webhook <url> para entregar")
		return nil
	}

	webhook, err := events.NewWebhook(webhookURL)
	if err != nil {
		return err
	}
	delivered, err := events.Drain(ctx, spool, webhook)
	// O número entregue é impresso mesmo em caso de erro: numa entrega parcial,
	// saber quantos passaram é o que diz se o destino está fora do ar ou se um
	// aviso específico é que não é aceito.
	fmt.Printf("avisos entregues: %d\n", delivered)
	return err
}
