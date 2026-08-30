package xai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// failureKind separa as três naturezas de falha que pedem tratamentos OPOSTOS.
//
// Confundi-las é caro nos dois sentidos: repetir um 401 gasta três chamadas
// contra uma chave que nunca vai funcionar, e comprimir o histórico por causa de
// uma queda de rede joga fora trabalho sem resolver nada.
type failureKind int

const (
	// kindPermanent: repetir dá exatamente o mesmo resultado. Chave inválida,
	// corpo malformado, modelo inexistente.
	kindPermanent failureKind = iota
	// kindTransient: rede, tempo esgotado, 429, 5xx. Repetir a MESMA chamada
	// costuma funcionar.
	kindTransient
	// kindContextTooLong: repetir igual dá o mesmo erro; quem refaz é o serviço,
	// com menos histórico.
	kindContextTooLong
)

// maxAttempts é o teto de tentativas de uma chamada ao modelo.
//
// Três, e não mais, porque o retry acontece com a TRAVA DA TELA na mão: cada
// espera é tela reservada por uma tarefa que não está fazendo nada. Com o
// backoff abaixo, o pior caso são ~6 s.
const maxAttempts = 3

// baseBackoff é a primeira espera; as seguintes dobram.
const baseBackoff = 2 * time.Second

// classifyStatus lê o código HTTP e o corpo para decidir a natureza da falha.
//
// A ORDEM dos testes importa. 429 e 5xx são transitórios mesmo quando o corpo
// menciona contexto — um servidor sobrecarregado pode devolver qualquer texto, e
// tratar isso como janela estourada faria o agente descartar histórico por causa
// de uma indisponibilidade passageira.
func classifyStatus(status int, body []byte) failureKind {
	switch {
	case status == http.StatusTooManyRequests, status >= 500:
		return kindTransient
	case status == http.StatusBadRequest, status == http.StatusRequestEntityTooLarge:
		// 400 é ambíguo por natureza: cabe tanto "seu JSON está errado" quanto
		// "seu prompt não cabe". Só aqui o corpo vira evidência, e só porque o
		// código sozinho não distingue.
		if mentionsContextLimit(body) {
			return kindContextTooLong
		}
		return kindPermanent
	default:
		return kindPermanent
	}
}

// mentionsContextLimit procura no corpo o vocabulário de janela estourada.
//
// É heurística de texto, e por isso fica confinada AQUI — no adaptador do
// fornecedor, onde o vocabulário dele é conhecido. O serviço recebe só o
// sentinela do porto e nunca vê estas palavras.
func mentionsContextLimit(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"context length", "context window", "maximum context",
		"too many tokens", "reduce the length", "context_length_exceeded",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// classifyTransport separa falha de rede de desistência de quem chamou.
//
// Cancelamento NÃO é falha transitória: se o operador apertou Ctrl+C, ou o
// contexto da tarefa estourou, repetir é gastar token contra a vontade dele. Por
// isso o contexto é consultado ANTES de olhar o erro — um `context.Canceled`
// embrulhado em erro de transporte pareceria queda de rede.
func classifyTransport(ctx context.Context, err error) failureKind {
	if ctx.Err() != nil {
		return kindPermanent
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return kindPermanent
	}
	// O que sobra é rede: conexão recusada, DNS, TLS, tempo esgotado do próprio
	// cliente HTTP. Tudo isso costuma passar na segunda tentativa.
	return kindTransient
}

// backoffFor devolve a espera antes da tentativa seguinte: 2 s, 4 s, 8 s...
func backoffFor(attempt int) time.Duration {
	return baseBackoff << attempt
}

// sleepCtx espera, mas acorda se o contexto for cancelado.
//
// `time.Sleep` puro seguraria a trava da tela até o fim do backoff mesmo depois
// de a tarefa ter sido cancelada — a tela ficaria reservada por alguém que já
// desistiu. Devolver o erro do contexto também impede a tentativa seguinte de
// sair contra um contexto morto.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
