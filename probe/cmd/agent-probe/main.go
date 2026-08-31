// Comando agent-probe: registra no kernel o que o modelo executa na máquina.
//
// É a camada que o `agentd` instrumentado NÃO consegue ser. O agente sabe a
// INTENÇÃO — esta ferramenta, neste turno, custou tanto — e é cego ao que o
// sistema de fato fez. Esta sabe o FATO — este binário rodou, com este pai,
// neste cgroup — e é cega à intenção. Juntas respondem "o modelo tentou X e o
// sistema fez Y", e só esta vale como prova, porque roda no kernel e o usuário
// `agent` não a desliga.
//
// O NOME é decisão de segurança, não estética: `agent-probe`, nunca
// `agentd-probe`. O sudoers concede ao usuário `agent` — logo ao modelo —
// `pkill -9 -f agentd*`, e o segundo nome casaria com esse padrão. Pelo mesmo
// motivo a unidade fica FORA das duas cujo journal o `agent` pode ler: o
// auditado não lê o log da auditoria.
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/andrebassi/agent-computer/probe/internal/collector"
	"github.com/andrebassi/agent-computer/probe/internal/decode"
	"github.com/andrebassi/agent-computer/probe/internal/sample"
	"github.com/andrebassi/agent-computer/probe/internal/shipper"
)

// bpfObject é o programa eBPF já compilado, embutido no binário.
//
// Compilado no Mac e COMMITADO, e isso só é possível porque o programa usa
// tracepoint em vez de kprobe com CO-RE: sem relocação dependente de BTF, o
// mesmo objeto carrega em qualquer kernel recente. A consequência prática é que
// a máquina nunca precisa de clang nem de LLVM — que ela não tem, e que o
// usuário `agent` não poderia instalar.
//
//go:embed exec.bpf.o
var execObject []byte

// netObject é o programa que observa conexão TCP de saída, embutido do mesmo
// jeito e pelo mesmo motivo.
//
//go:embed net.bpf.o
var netObject []byte

// exitFailure é o código de saída quando o coletor não consegue trabalhar.
const exitFailure = 1

// main lê os parâmetros e delega, com um único ponto de saída para o erro.
func main() {
	var (
		sink = flag.String("sink", "",
			"URL de ingestão dos eventos; vazio só imprime na saída padrão")
		sinkFile = flag.String("sink-file", "",
			"arquivo com a URL de ingestão; vence o -sink quando tem conteúdo")
		verbose = flag.Bool("verbose", false,
			"imprime cada evento além de enviá-lo")
	)
	flag.Parse()

	if err := run(resolveSink(*sink, *sinkFile), *verbose); err != nil {
		fmt.Fprintf(os.Stderr, "erro: %v\n", err)
		os.Exit(exitFailure)
	}
}

// resolveSink escolhe o destino entre a flag e o arquivo.
//
// O ARQUIVO vence quando tem conteúdo, e é ele que a unidade systemd usa. Três
// motivos, todos vindos da revisão de segurança deste repositório:
//
//   - muda sem rebuild do sistema, o que importa num NixOS onde tudo o mais é
//     declarativo e a alternativa seria um ciclo de minutos para trocar uma URL;
//   - mora em /etc, fora do alcance do modelo — um caminho gravável por ele foi
//     exatamente a escalada do achado 3, em que um EnvironmentFile em
//     /workspace desligou o rebaixamento das ferramentas;
//   - é lido pelo PRÓPRIO binário, então a unidade não precisa de shell para
//     montar o argumento, que é o achado 4.
//
// Arquivo ausente ou vazio não é erro: é o estado padrão de uma máquina recém
// criada, e nele o coletor sobe e imprime, mantendo o registro no journal.
func resolveSink(sink, sinkFile string) string {
	if sinkFile == "" {
		return sink
	}
	content, err := os.ReadFile(sinkFile)
	if err != nil {
		// Sem aviso em stderr: o arquivo ausente é o caso NORMAL no primeiro
		// boot, e um alarme a cada subida treina a ignorar o log da unidade.
		return sink
	}
	if trimmed := strings.TrimSpace(string(content)); trimmed != "" {
		return trimmed
	}
	return sink
}

// run carrega o programa, drena o ring buffer e entrega em lote.
//
// As duas metades rodam em goroutines SEPARADAS de propósito, e essa separação
// é a decisão central: quem lê o kernel nunca toca em rede. Se tocasse, uma
// entrega lenta encheria o ring buffer do KERNEL — e a perda passaria a
// acontecer no único lugar onde ela não pode ser contada.
func run(sink string, verbose bool) error {
	bootTime := collector.BootTime()
	sender := shipper.New(sink, bootTime)

	probe, err := collector.Open(execObject, netObject)
	if err != nil {
		return err
	}
	defer probe.Close()

	// SIGTERM é o que o systemd manda ao parar a unidade. Sem tratá-lo, o
	// encerramento perderia o lote pendente — e o lote pendente na hora de
	// parar é justamente o que aconteceu por último.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("agent-probe no ar: sched_process_exec atachado, destino=%q\n", sinkLabel(sink))

	go flushLoop(ctx, sender)
	go healthLoop(ctx, sender)

	err = probe.Run(ctx,
		func(event decode.ExecEvent) {
			if verbose || sender == nil {
				fmt.Printf("%s exec uid=%d pid=%d cgroup=%d comm=%s %s\n",
					event.WallClock(bootTime).UTC().Format(time.RFC3339),
					event.UID, event.PID, event.CgroupID, event.Comm, event.Filename)
			}
			sender.Add(event)
		},
		func(event decode.NetEvent) {
			if verbose || sender == nil {
				// O destino privado é marcado na própria linha: é o que separa
				// "chamou a API do modelo" de "varreu a sub-rede" sem quem lê
				// ter de reconhecer faixas de endereço de cabeça.
				scope := "publico"
				if event.IsPrivateDestination() {
					scope = "PRIVADO"
				}
				fmt.Printf("%s conn uid=%d pid=%d comm=%s -> %s:%d (%s)\n",
					event.WallClock(bootTime).UTC().Format(time.RFC3339),
					event.UID, event.PID, event.Comm,
					event.Destination, event.DestinationPort, scope)
			}
			sender.AddNet(event)
		})
	if err != nil {
		return err
	}

	// Última entrega, com prazo PRÓPRIO e curto.
	//
	// O contexto de execução já foi cancelado neste ponto, então reusá-lo faria
	// a entrega falhar na hora. E o prazo é curto porque o systemd está
	// contando: telemetria não pode consumir o tempo que o encerramento
	// limpo tem para acontecer.
	finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sender.Flush(finalCtx); err != nil {
		fmt.Fprintf(os.Stderr, "aviso: a última entrega falhou: %v\n", err)
	}
	reportDrops(sender)
	return nil
}

// healthSampleInterval é de quanto em quanto tempo a saúde é amostrada.
//
// 30 segundos, e o número vem da natureza do sinal: o PSI do kernel já expõe
// médias de 10, 60 e 300 segundos, então amostrar mais rápido que a menor delas
// só produziria o mesmo valor repetido. Mais devagar perderia o pico curto, que
// é justamente o que uma tela travando produz.
const healthSampleInterval = 30 * time.Second

// procRoot é onde o kernel expõe a saúde da máquina.
//
// Constante em vez de flag: não há caso de uso para mudá-la em produção, e o
// teste do pacote `sample` já recebe a raiz por parâmetro.
const procRoot = "/proc"

// healthLoop amostra a saúde da máquina em intervalo fixo.
//
// Isto NÃO é eBPF, e a distinção é deliberada. O kernel já calcula a pressão em
// /proc/pressure; uma probe que a recalculasse a partir de eventos seria mais
// cara e menos precisa. A divisão de trabalho é a tese do coletor: PSI diz QUE
// a máquina degradou e QUANTO, as probes dizem QUEM causou.
func healthLoop(ctx context.Context, sender *shipper.Shipper) {
	ticker := time.NewTicker(healthSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			health, err := sample.Read(procRoot)
			if err != nil {
				// Uma amostra perdida não derruba nada: a próxima vem em 30s, e
				// parar o coletor por causa da métrica opcional seria trocar o
				// sinal principal pelo secundário.
				fmt.Fprintf(os.Stderr, "aviso: amostra de saúde falhou: %v\n", err)
				continue
			}
			sender.AddHealth(health)
		}
	}
}

// flushLoop entrega o lote pendente em intervalo fixo.
func flushLoop(ctx context.Context, sender *shipper.Shipper) {
	ticker := time.NewTicker(shipper.FlushInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sender.Flush(ctx); err != nil {
				// Falha de entrega é ESPERADA: o backend roda num laptop que
				// vai estar fechado. Avisa e segue — o lote fica na fila, e o
				// teto de buffer é quem impede o crescimento sem fim.
				fmt.Fprintf(os.Stderr, "aviso: %v\n", err)
			}
		}
	}
}

// reportDrops imprime quantos eventos se perderam por buffer cheio.
//
// Sempre, inclusive quando é zero. Perda silenciosa é o defeito que este
// coletor existe para não ter: sem esta linha, "o modelo não executou nada" e
// "descartei o que ele executou" ficam indistinguíveis — e a segunda é
// exatamente o que um adversário quer que pareça a primeira.
func reportDrops(sender *shipper.Shipper) {
	fmt.Printf("encerrando: %d evento(s) descartado(s) por buffer cheio\n", sender.Dropped())
}

// sinkLabel descreve o destino para a linha de subida.
func sinkLabel(sink string) string {
	if sink == "" {
		return "nenhum (só imprime)"
	}
	return sink
}
