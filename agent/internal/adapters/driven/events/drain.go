package events

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// Drain entrega os fatos enfileirados e limpa a fila.
//
// Roda num PROCESSO SEPARADO, chamado por timer. É essa separação que satisfaz o
// requisito duro: a entrega não depende da conexão que iniciou a tarefa, nem
// mantém o agente esperando um serviço remoto responder. Nada precisa ser
// derrubado para que um aviso saia.
//
// Devolve quantos foram entregues e o primeiro erro encontrado.
//
// ⚠️ A fila só é limpa quando TUDO foi entregue. Limpar depois de uma entrega
// parcial perderia os fatos restantes — e o mais provável de falhar é o mais
// recente, que costuma ser o mais urgente. A consequência aceita é a oposta:
// um aviso pode ser entregue DUAS vezes se a falha vier depois dele. Aviso
// repetido incomoda; aviso perdido deixa uma tela travada sem ninguém saber.
func Drain(ctx context.Context, spool *Spool, sink ports.EventSink) (int, error) {
	pending, err := spool.Pending(ctx)
	if err != nil {
		return 0, fmt.Errorf("lendo fila: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	delivered := 0
	var firstErr error
	var partials []string
	for _, event := range pending {
		err := sink.Publish(ctx, event)
		// Entrega PARCIAL não segura a fila: o aviso já chegou a alguém, e
		// reenviá-lo por causa de um destino quebrado inundaria de duplicatas
		// justamente o destino que funciona — até quem recebe silenciar o canal.
		var partial *PartialDelivery
		if errors.As(err, &partial) {
			partials = append(partials, partial.Error())
			delivered++
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("entregando %s: %w", event.TaskID, err)
			}
			continue
		}
		delivered++
	}

	if firstErr != nil {
		// Fila intacta: a próxima passada tenta de novo.
		return delivered, firstErr
	}
	if err := spool.Clear(ctx); err != nil {
		return delivered, fmt.Errorf("limpando fila: %w", err)
	}
	if len(partials) > 0 {
		// A fila JÁ foi limpa. Isto volta como aviso para aparecer no log do
		// serviço, não como falha que faria o systemd marcar a unidade em erro.
		return delivered, &PartialDelivery{Delivered: delivered, Failures: partials}
	}
	return delivered, nil
}

// Describe rende as linhas humanas dos fatos pendentes.
//
// Existe para o drenador poder ser rodado à mão sem destino configurado — ver o
// que está esperando é útil por si só, e é a primeira coisa que alguém faz
// quando desconfia que um aviso se perdeu.
func Describe(events []domain.TaskEvent) []string {
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, event.Message())
	}
	return lines
}
