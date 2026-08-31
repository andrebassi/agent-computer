package main

import (
	"context"
	"errors"
	"fmt"
	"os"

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

	// O formato vem do ambiente, junto do destino: quem configura o webhook é
	// quem sabe o que há do outro lado. Valor ausente ou desconhecido é `raw`,
	// que é como o destino sempre funcionou.
	fallback := events.ParseWebhookFormat(os.Getenv("AGENT_WEBHOOK_FORMAT"))
	// A lista aceita um destino só — que é o caso de sempre — ou vários,
	// separados por vírgula, cada um com o seu formato.
	sink, err := events.NewMultiSink(webhookURL, fallback)
	if err != nil {
		return err
	}
	if n := sink.Destinations(); n > 1 {
		fmt.Printf("destinos configurados: %d\n", n)
	}

	delivered, err := events.Drain(ctx, spool, sink)
	// O número entregue é impresso mesmo em caso de erro: numa entrega parcial,
	// saber quantos passaram é o que diz se o destino está fora do ar ou se um
	// aviso específico é que não é aceito.
	fmt.Printf("avisos entregues: %d\n", delivered)

	// Falha em PARTE dos destinos não derruba o comando: o aviso chegou a
	// alguém e a fila já foi limpa. Sair com erro aqui faria o systemd marcar a
	// unidade como falha a cada passada, e um vermelho permanente é ignorado —
	// junto com o vermelho de verdade, quando ele vier.
	var partial *events.PartialDelivery
	if errors.As(err, &partial) {
		fmt.Printf("⚠️  %s\n", partial.Error())
		return nil
	}
	return err
}
