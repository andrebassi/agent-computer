//go:build !linux

// Este arquivo existe para que `go build ./...` e `go vet ./...` funcionem no
// Mac onde o coletor é escrito.
//
// Sem ele, todo comando de verificação local falharia na primeira linha, e a
// resposta natural seria parar de rodá-los — perdendo a checagem de tipos dos
// pacotes que NÃO dependem de Linux (o decodificador e o remetente, que são
// justamente onde os defeitos moram).
package collector

import (
	"context"
	"errors"
	"time"

	"github.com/andrebassi/agent-computer/probe/internal/decode"
)

// ErrUnsupportedPlatform diz que não há eBPF fora do Linux.
//
// Erro claro em vez de falha de compilação: quem rodar o binário no Mac por
// engano recebe uma frase que explica, em vez de descobrir que o build nunca
// existiu para esta plataforma.
var ErrUnsupportedPlatform = errors.New("o coletor eBPF só roda em Linux")

// Collector é o equivalente vazio fora do Linux.
type Collector struct{}

// Handler mantém a mesma assinatura, para o chamador não precisar de build tag.
type Handler func(decode.ExecEvent)

// NetHandler mantém a mesma assinatura fora do Linux.
type NetHandler func(decode.NetEvent)

// Open sempre recusa fora do Linux.
func Open([]byte, []byte) (*Collector, error) { return nil, ErrUnsupportedPlatform }

// Run sempre recusa fora do Linux.
func (c *Collector) Run(context.Context, Handler, NetHandler) error { return ErrUnsupportedPlatform }

// Close não tem o que desfazer fora do Linux.
func (c *Collector) Close() {}

// BootTime devolve o instante atual fora do Linux.
//
// Não há `CLOCK_BOOTTIME` portável aqui, e o valor não é usado: sem coletor não
// há evento para carimbar.
func BootTime() time.Time { return time.Now() }
