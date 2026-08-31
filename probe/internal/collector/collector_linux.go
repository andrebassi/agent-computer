//go:build linux

// Package collector carrega o programa eBPF, o atacha ao tracepoint e drena o
// ring buffer.
//
// É o único pacote deste módulo que exige um kernel Linux e privilégio, e por
// isso o único que NÃO tem como ser testado no Mac onde ele é escrito. A
// exclusão está declarada no gate de cobertura, junto com onde a prova mora:
// `scripts/46-ebpf-test.sh`, na máquina, com gatilho determinístico e prova de
// falha nos dois sentidos.
package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
	"golang.org/x/sys/unix"

	"github.com/andrebassi/agent-computer/probe/internal/decode"
)

// Collector mantém os programas carregados e os leitores dos ring buffers.
type Collector struct {
	collection  *ebpf.Collection
	attachments []link.Link
	execReader  *ringbuf.Reader
	netReader   *ringbuf.Reader
}

// Handler recebe cada `execve` decodificado.
//
// Roda no MESMO laço que drena o ring buffer, então precisa ser rápido e não
// pode tocar em rede: uma operação lenta aqui se propaga para trás até o buffer
// do kernel, onde a perda vira invisível.
type Handler func(decode.ExecEvent)

// NetHandler recebe cada conexão TCP decodificada.
type NetHandler func(decode.NetEvent)

// Open carrega o objeto BPF, atacha o tracepoint e prepara a leitura.
//
// A ordem é deliberada: o limite de memória travada primeiro (sem ele o
// carregamento falha com um erro de permissão que não diz o motivo), depois o
// programa, depois o atach. Cada passo desfaz os anteriores se falhar — um
// programa carregado e não atachado fica ocupando memória do kernel até o
// processo morrer.
func Open(execObject, netObject []byte) (*Collector, error) {
	// Em kernels anteriores ao 5.11 os mapas BPF eram contabilizados no limite
	// de memória travada do processo, e o padrão de 64 KiB não cobre um ring
	// buffer de 256 KiB. Em 5.11+ isso virou contabilidade de memcg e a chamada
	// é inócua — mantê-la custa nada e evita uma falha obscura em kernel velho.
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("liberando o limite de memória travada: %w", err)
	}

	// Os dois objetos são carregados numa coleção só. Programas separados, mas
	// um ciclo de vida: fechar metade deixaria o outro programa atachado no
	// kernel até o processo morrer.
	spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(execObject))
	if err != nil {
		return nil, fmt.Errorf("lendo o objeto BPF de exec: %w", err)
	}
	netSpec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(netObject))
	if err != nil {
		return nil, fmt.Errorf("lendo o objeto BPF de rede: %w", err)
	}
	for name, program := range netSpec.Programs {
		spec.Programs[name] = program
	}
	for name, mapSpec := range netSpec.Maps {
		spec.Maps[name] = mapSpec
	}

	collection, err := ebpf.NewCollection(spec)
	if err != nil {
		// O erro do verificador do kernel é a informação mais valiosa quando
		// isto falha, e ele vem num campo separado do erro. Sem `%+v` a
		// mensagem sai como "argument list too long" ou "invalid argument" e o
		// diagnóstico começa do zero.
		return nil, fmt.Errorf("carregando os programas no kernel: %+v", err)
	}

	collector := &Collector{collection: collection}

	// Cada atach é desfeito se um posterior falhar. Um programa carregado e não
	// atachado fica ocupando memória do kernel até o processo morrer.
	attachments := []struct {
		group, name, program string
	}{
		{"sched", "sched_process_exec", "handle_exec"},
		{"sock", "inet_sock_set_state", "handle_connect"},
	}
	for _, item := range attachments {
		program, ok := collection.Programs[item.program]
		if !ok {
			collector.Close()
			return nil, fmt.Errorf("o objeto não traz o programa %s", item.program)
		}
		attachment, err := link.Tracepoint(item.group, item.name, program, nil)
		if err != nil {
			collector.Close()
			return nil, fmt.Errorf("atachando a %s:%s: %w", item.group, item.name, err)
		}
		collector.attachments = append(collector.attachments, attachment)
	}

	// Dois ring buffers, e não um: os volumes são muito diferentes, e um buffer
	// compartilhado faria uma rajada de conexões descartar eventos de exec —
	// perdendo o registro mais importante por causa do mais frequente.
	if collector.execReader, err = openRing(collection, "events"); err != nil {
		collector.Close()
		return nil, err
	}
	if collector.netReader, err = openRing(collection, "net_events"); err != nil {
		collector.Close()
		return nil, err
	}
	return collector, nil
}

// openRing abre o leitor de um mapa de ring buffer pelo nome.
func openRing(collection *ebpf.Collection, name string) (*ringbuf.Reader, error) {
	events, ok := collection.Maps[name]
	if !ok {
		return nil, fmt.Errorf("o objeto não traz o mapa %s", name)
	}
	reader, err := ringbuf.NewReader(events)
	if err != nil {
		return nil, fmt.Errorf("abrindo o ring buffer %s: %w", name, err)
	}
	return reader, nil
}

// Run drena o ring buffer até o contexto ser cancelado.
//
// A goroutine que fecha o leitor é o que faz o cancelamento funcionar:
// `reader.Read` bloqueia no kernel e não observa contexto. Fechar o leitor faz
// a leitura pendente devolver o sentinela de fechamento, que é o sinal de
// parada limpa — sem isso, o encerramento só aconteceria no próximo evento, que
// numa máquina ociosa pode demorar minutos.
func (c *Collector) Run(ctx context.Context, onExec Handler, onNet NetHandler) error {
	go func() {
		<-ctx.Done()
		_ = c.execReader.Close()
		_ = c.netReader.Close()
	}()

	// O ring de rede é drenado em goroutine PRÓPRIA. Alternar entre os dois num
	// laço só exigiria leitura não-bloqueante, e a espera de um encheria o
	// outro: são volumes muito diferentes, e o de rede tem rajadas.
	netDone := make(chan struct{})
	go func() {
		defer close(netDone)
		for {
			record, err := c.netReader.Read()
			if err != nil {
				return
			}
			event, err := decode.DecodeNet(record.RawSample)
			if err != nil {
				continue
			}
			onNet(event)
		}
	}()

	err := c.drainExec(onExec)
	<-netDone
	return err
}

// drainExec lê o ring de `execve` até ele ser fechado.
func (c *Collector) drainExec(handler Handler) error {
	for {
		record, err := c.execReader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return nil
			}
			return fmt.Errorf("lendo do ring buffer de exec: %w", err)
		}

		event, err := decode.Decode(record.RawSample)
		if err != nil {
			// Registro malformado NÃO derruba o coletor: perder um evento é
			// muito menos grave que parar de coletar todos os seguintes. O laço
			// segue, e o defeito aparece na contagem, não num serviço morto.
			continue
		}
		handler(event)
	}
}

// Close desfaz tudo, na ordem inversa da abertura.
func (c *Collector) Close() {
	if c.execReader != nil {
		_ = c.execReader.Close()
	}
	if c.netReader != nil {
		_ = c.netReader.Close()
	}
	for _, attachment := range c.attachments {
		_ = attachment.Close()
	}
	if c.collection != nil {
		c.collection.Close()
	}
}

// BootTime estima o instante do boot, para converter o relógio monotônico.
//
// O evento carrega `bpf_ktime_get_ns`, que conta desde o boot. Subtrair o tempo
// de atividade do relógio atual dá a referência.
//
// A estimativa é feita UMA vez, na subida, e não a cada evento: recalcular
// produziria carimbos que andam para trás quando os dois relógios divergem, e
// tempo não-monotônico num registro de auditoria destrói a única coisa que ele
// garante, que é a ordem.
func BootTime() time.Time {
	var uptime unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &uptime); err != nil {
		// Sem o relógio do kernel, o melhor palpite é agora. Os carimbos ficam
		// deslocados, mas os INTERVALOS entre eventos continuam corretos — e é
		// o intervalo que responde "o que rodou durante esta ferramenta".
		return time.Now()
	}
	return time.Now().Add(-time.Duration(uptime.Nano()))
}
