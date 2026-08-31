package telemetry

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// newRecordingMeter monta um medidor que guarda tudo em memória.
//
// Sem rede e sem backend: o que se quer verificar é o que o adaptador PRODUZ, e
// um coletor de verdade no meio só acrescentaria intermitência.
func newRecordingMeter(t *testing.T) (*Meter, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	return newMeterFrom(provider.Meter("teste")), reader
}

// collectNames lista os instrumentos que o leitor coletou.
func collectNames(t *testing.T, reader *sdkmetric.ManualReader) []string {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("coleta falhou: %v", err)
	}
	var names []string
	for _, scope := range collected.ScopeMetrics {
		for _, item := range scope.Metrics {
			names = append(names, item.Name)
		}
	}
	return names
}

// hasName diz se o instrumento foi coletado.
func hasName(names []string, wanted string) bool {
	for _, name := range names {
		if name == wanted {
			return true
		}
	}
	return false
}

// TestMeterRecordsEveryInstrumentType prova que os quatro tipos chegam ao
// backend com o nome certo.
//
// Os quatro existem por razões diferentes — contador para o que só cresce,
// histograma para distribuição, medidor para valor corrente —, e usar o tipo
// errado produz um gráfico que parece certo e responde outra pergunta.
func TestMeterRecordsEveryInstrumentType(t *testing.T) {
	meter, reader := newRecordingMeter(t)
	ctx := context.Background()

	meter.AddCount(ctx, "agentd.model.tokens", 100, service.String("agentd.token.type", "input"))
	meter.AddFloat(ctx, "agentd.model.cost.usd", 0.0016)
	meter.RecordDuration(ctx, "agentd.tool.duration", 0.029, service.String("agentd.tool.name", "shell"))
	meter.AddUpDown(ctx, "agentd.tasks.running", 1)

	names := collectNames(t, reader)
	for _, wanted := range []string{
		"agentd.model.tokens", "agentd.model.cost.usd",
		"agentd.tool.duration", "agentd.tasks.running",
	} {
		if !hasName(names, wanted) {
			t.Errorf("instrumento %q não chegou ao backend\n  presentes: %v", wanted, names)
		}
	}
}

// TestDurationCarriesSecondsUnit prova que a unidade vai declarada.
//
// Sem ela o backend não sabe formatar o eixo, e um gráfico que mostra "0.029"
// sem dizer de quê é um gráfico que ninguém interpreta na urgência.
func TestDurationCarriesSecondsUnit(t *testing.T) {
	meter, reader := newRecordingMeter(t)
	meter.RecordDuration(context.Background(), "agentd.tool.duration", 0.029)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("coleta falhou: %v", err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name != "agentd.tool.duration" {
				continue
			}
			if item.Unit != "s" {
				t.Errorf("unidade: got %q, want %q", item.Unit, "s")
			}
			return
		}
	}
	t.Fatal("o histograma não foi coletado")
}

// TestMeterReusesInstruments prova que o instrumento é criado UMA vez.
//
// O SDK não deduplica: chamar `Int64Counter` a cada turno produziria um objeto
// novo por chamada. Numa tarefa de 60 iterações isso é sessenta alocações por
// contador, e o custo apareceria como CPU do agente sem nenhuma pista da causa.
func TestMeterReusesInstruments(t *testing.T) {
	meter, _ := newRecordingMeter(t)
	ctx := context.Background()

	for index := 0; index < 50; index++ {
		meter.AddCount(ctx, "agentd.model.tokens", 1)
		meter.RecordDuration(ctx, "agentd.tool.duration", 0.01)
		meter.AddFloat(ctx, "agentd.model.cost.usd", 0.001)
		meter.AddUpDown(ctx, "agentd.tasks.running", 1)
	}

	meter.mutex.Lock()
	defer meter.mutex.Unlock()
	sizes := map[string]int{
		"contadores":  len(meter.counters),
		"fracionário": len(meter.floats),
		"histogramas": len(meter.histograms),
		"sobe-desce":  len(meter.upDowns),
	}
	for kind, size := range sizes {
		if size != 1 {
			t.Errorf("%s criados: %d, esperado 1 — o cache não está sendo usado", kind, size)
		}
	}
}

// TestMeterIsSafeUnderConcurrency exercita o laço paralelo de ferramentas.
//
// `runToolCalls` executa ferramentas em goroutines, e cada uma mede sua
// duração. A suíte roda com -race, e é aqui que a ausência de trava no cache de
// instrumentos apareceria.
func TestMeterIsSafeUnderConcurrency(t *testing.T) {
	meter, _ := newRecordingMeter(t)
	ctx := context.Background()

	var waitGroup sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for index := 0; index < 100; index++ {
				meter.AddCount(ctx, "agentd.model.tokens", 1)
				meter.RecordDuration(ctx, "agentd.tool.duration", 0.01)
				meter.AddUpDown(ctx, "agentd.tasks.running", 1)
				meter.AddFloat(ctx, "agentd.model.cost.usd", 0.001)
			}
		}()
	}
	waitGroup.Wait()
}

// TestInvalidInstrumentNameIsSilent cobre o ramo de degradação dos quatro tipos.
//
// O SDK recusa nome de instrumento fora da sintaxe do OpenTelemetry. Quando
// isso acontece, o adaptador VOLTA EM SILÊNCIO em vez de entrar em pânico —
// derrubar a tarefa por causa de um contador seria a observação matando o
// observado.
//
// Sem este teste o ramo seria código morto, e código morto num caminho de falha
// só se descobre no dia em que ele precisava ter funcionado.
func TestInvalidInstrumentNameIsSilent(t *testing.T) {
	meter, reader := newRecordingMeter(t)
	ctx := context.Background()

	// Espaço não é caractere válido em nome de instrumento.
	const invalid = "nome com espaço"
	meter.AddCount(ctx, invalid, 1)
	meter.AddFloat(ctx, invalid, 1.0)
	meter.RecordDuration(ctx, invalid, 0.1)
	meter.AddUpDown(ctx, invalid, 1)

	if names := collectNames(t, reader); hasName(names, invalid) {
		t.Errorf("o instrumento inválido foi registrado assim mesmo: %v", names)
	}

	// E o cache não guardou nada: senão a segunda chamada usaria um instrumento
	// que o SDK recusou, e o defeito viraria permanente.
	meter.mutex.Lock()
	defer meter.mutex.Unlock()
	if len(meter.counters)+len(meter.floats)+len(meter.histograms)+len(meter.upDowns) != 0 {
		t.Error("o cache guardou um instrumento que o SDK recusou")
	}
}

// TestNewMeterWithoutEndpointIsNotAnError cobre o padrão da máquina.
//
// Sem `AGENTD_OTLP_ENDPOINT` o agente roda igual. Tratar isso como erro faria o
// `buildDeps` avisar em toda execução, e um aviso constante treina a ignorar o
// aviso — inclusive quando ele for de defeito real.
func TestNewMeterWithoutEndpointIsNotAnError(t *testing.T) {
	meter, shutdown, err := NewMeter(context.Background(), "", "agentd", "0.0.0")
	if err != nil {
		t.Fatalf("endpoint vazio devolveu erro: %v", err)
	}
	if meter != nil {
		t.Fatal("sem endpoint o medidor tem que ser nil, para o serviço usar o mudo dele")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("o encerrador no-op devolveu erro: %v", err)
	}
}

// TestNewMeterDoesNotBlockOnDial prova a propriedade que sustenta tudo.
//
// O backend roda num laptop que vai estar fechado. Se a criação bloqueasse
// esperando conexão, o `agentd` não subiria justamente nas horas em que
// ninguém está olhando.
func TestNewMeterDoesNotBlockOnDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("não consegui reservar uma porta: %v", err)
	}
	closedAddress := listener.Addr().String()
	_ = listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		meter, shutdown, err := NewMeter(context.Background(), closedAddress, "agentd", "0.0.0")
		if err != nil || meter == nil {
			return
		}
		meter.AddCount(context.Background(), "agentd.model.tokens", 1)
		ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout())
		defer cancel()
		_ = shutdown(ctx)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("NewMeter bloqueou com o backend fora do ar; o agente não subiria")
	}
}

// TestNewMeterWithInvalidAddressReturnsError cobre o ramo de degradação.
//
// Sem ele o caminho "avisa e segue com o medidor mudo" seria código morto — e
// código morto num caminho de falha só se descobre no dia em que precisava ter
// funcionado.
func TestNewMeterWithInvalidAddressReturnsError(t *testing.T) {
	if _, _, err := NewMeter(context.Background(), "\x00invalid-address", "agentd", "0.0.0"); err == nil {
		t.Fatal("endereço inválido não produziu erro")
	}
}
