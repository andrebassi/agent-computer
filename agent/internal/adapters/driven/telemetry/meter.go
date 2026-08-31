package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// metricExportInterval é de quanto em quanto tempo as métricas são enviadas.
//
// Métrica não é evento: o SDK acumula localmente e exporta o estado periódico,
// então este intervalo é resolução, não latência de entrega. Trinta segundos dá
// um ponto por meio minuto — o suficiente para ver a curva de custo de uma
// tarefa longa sem inflar o número de amostras de uma máquina que fica ociosa a
// maior parte do tempo.
const metricExportInterval = 30 * time.Second

// Meter implementa service.Meter sobre o SDK do OpenTelemetry.
//
// Os instrumentos são criados sob demanda e guardados, porque criar um
// instrumento é caro e o SDK não deduplica: chamar `Int64Counter` a cada turno
// produziria um objeto novo por chamada, e o custo apareceria como CPU do
// agente sem nenhuma pista de onde veio.
type Meter struct {
	meter metric.Meter

	mutex      sync.Mutex
	counters   map[string]metric.Int64Counter
	floats     map[string]metric.Float64Counter
	histograms map[string]metric.Float64Histogram
	upDowns    map[string]metric.Int64UpDownCounter
}

// NewMeter monta o medidor e devolve o encerrador.
//
// Endpoint vazio devolve (nil, no-op, nil), como o rastreador: sem backend
// configurado o agente roda igual, com o medidor mudo do serviço.
func NewMeter(ctx context.Context, endpoint, serviceName, serviceVersion string) (*Meter, Shutdown, error) {
	if endpoint == "" {
		return nil, func(context.Context) error { return nil }, nil
	}

	// HTTP, e não gRPC — e o motivo é concreto, medido em 31/08/2026.
	//
	// Traces e métricas vão para BACKENDS DIFERENTES neste desenho: os traces
	// para o VictoriaTraces, as métricas para o VictoriaMetrics. Apontar as duas
	// para o mesmo endpoint gRPC produz um erro que parece de rede e não é:
	//
	//   rpc error: code = Unimplemented
	//   desc = gRPC method not found: .../MetricsService/Export
	//
	// O VictoriaTraces implementa só o serviço de traces. O VictoriaMetrics
	// recebe métricas OTLP em HTTP, no caminho `/opentelemetry/v1/metrics`.
	exporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithURLPath("/opentelemetry/v1/metrics"),
		otlpmetrichttp.WithInsecure(),
		otlpmetrichttp.WithTimeout(exportTimeout),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("criando exportador OTLP de métricas: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(metricExportInterval),
			sdkmetric.WithTimeout(exportTimeout),
		)),
		// O mesmo recurso montado à mão do rastreador, pelo mesmo motivo de
		// segurança: os detectores automáticos recolhem `process.command_args`,
		// e a linha de comando não pode sair da máquina.
		sdkmetric.WithResource(buildResource(serviceName, serviceVersion)),
	)

	return newMeterFrom(provider.Meter(serviceName)), provider.Shutdown, nil
}

// newMeterFrom monta o medidor sobre um `metric.Meter` já pronto.
//
// Separado de `NewMeter` para o teste poder injetar um provedor com leitor em
// memória, sem exportador nem rede. Sem isto, verificar o que o adaptador
// produz exigiria um coletor OTLP de verdade no meio — uma fonte de
// intermitência para provar aritmética de mapa.
func newMeterFrom(meter metric.Meter) *Meter {
	return &Meter{
		meter:      meter,
		counters:   make(map[string]metric.Int64Counter),
		floats:     make(map[string]metric.Float64Counter),
		histograms: make(map[string]metric.Float64Histogram),
		upDowns:    make(map[string]metric.Int64UpDownCounter),
	}
}

// AddCount soma a um contador monotônico.
func (m *Meter) AddCount(ctx context.Context, name string, value int64, attributes ...service.Attribute) {
	m.mutex.Lock()
	counter, ok := m.counters[name]
	if !ok {
		created, err := m.meter.Int64Counter(name)
		if err != nil {
			// Instrumento que não pôde ser criado vira silêncio, não pânico:
			// derrubar a tarefa por causa de um contador seria a observação
			// matando o observado.
			m.mutex.Unlock()
			return
		}
		m.counters[name] = created
		counter = created
	}
	m.mutex.Unlock()
	counter.Add(ctx, value, metric.WithAttributes(convert(attributes)...))
}

// AddFloat soma a um contador monotônico fracionário.
func (m *Meter) AddFloat(ctx context.Context, name string, value float64, attributes ...service.Attribute) {
	m.mutex.Lock()
	counter, ok := m.floats[name]
	if !ok {
		created, err := m.meter.Float64Counter(name)
		if err != nil {
			m.mutex.Unlock()
			return
		}
		m.floats[name] = created
		counter = created
	}
	m.mutex.Unlock()
	counter.Add(ctx, value, metric.WithAttributes(convert(attributes)...))
}

// RecordDuration guarda uma duração num histograma, em segundos.
//
// A unidade vai declarada (`s`), e não é enfeite: sem ela o backend não sabe
// formatar o eixo, e um gráfico que mostra "0.029" sem dizer de quê é um
// gráfico que ninguém interpreta na urgência.
func (m *Meter) RecordDuration(ctx context.Context, name string, seconds float64, attributes ...service.Attribute) {
	m.mutex.Lock()
	histogram, ok := m.histograms[name]
	if !ok {
		created, err := m.meter.Float64Histogram(name, metric.WithUnit("s"))
		if err != nil {
			m.mutex.Unlock()
			return
		}
		m.histograms[name] = created
		histogram = created
	}
	m.mutex.Unlock()
	histogram.Record(ctx, seconds, metric.WithAttributes(convert(attributes)...))
}

// AddUpDown soma a um contador que sobe e desce.
func (m *Meter) AddUpDown(ctx context.Context, name string, value int64, attributes ...service.Attribute) {
	m.mutex.Lock()
	counter, ok := m.upDowns[name]
	if !ok {
		created, err := m.meter.Int64UpDownCounter(name)
		if err != nil {
			m.mutex.Unlock()
			return
		}
		m.upDowns[name] = created
		counter = created
	}
	m.mutex.Unlock()
	counter.Add(ctx, value, metric.WithAttributes(convert(attributes)...))
}
