// Package telemetry liga o porto Tracer do serviço ao OpenTelemetry.
//
// É o ÚNICO lugar do agente que conhece o SDK do OpenTelemetry. `service` e
// `domain` seguem sem nenhum import de terceiro, e trocar de backend — ou de
// biblioteca — não toca em regra de negócio.
package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// exportTimeout é o teto de uma tentativa de envio ao backend.
//
// O número não é arbitrário: precisa ser MENOR que qualquer tempo de ferramenta
// para o envio nunca virar o gargalo do turno. Os tetos existentes são 45s no
// CDP, 2min no shell, 5min no modelo e 15min na delegação. Cinco segundos passa
// longe de todos, e é folga suficiente para um túnel lento.
const exportTimeout = 5 * time.Second

// shutdownTimeout é o teto para esvaziar a fila ao encerrar.
//
// A unidade systemd concede 40s antes do SIGKILL, e esses 40s são para as
// TAREFAS gravarem estado e soltarem a trava. Telemetria não pode consumir esse
// orçamento: com o Mac desligado, o envio nunca completa, e esperar por ele
// atrasaria o encerramento de quem tem trabalho real a salvar.
const shutdownTimeout = 3 * time.Second

// Tracer implementa service.Tracer sobre o SDK do OpenTelemetry.
type Tracer struct {
	tracer oteltrace.Tracer
}

// Shutdown esvazia a fila e fecha o exportador.
type Shutdown func(context.Context) error

// New monta o rastreador e devolve o encerrador.
//
// O endpoint vazio é caso legítimo, não erro: devolve (nil, no-op, nil), e o
// chamador cai no rastreador mudo do serviço. É o que faz a telemetria ser
// opcional de verdade — sem ela configurada, o agente roda igual.
func New(ctx context.Context, endpoint, serviceName, serviceVersion string) (*Tracer, Shutdown, error) {
	if endpoint == "" {
		return nil, func(context.Context) error { return nil }, nil
	}

	// WithInsecure porque o caminho até o backend é túnel SSH ou malha
	// Tailscale, que já cifram. TLS por cima cifraria de novo e traria
	// certificado para gerenciar numa máquina descartável.
	//
	// SEM a opção de bloqueio: com ela, subir o agente com o Mac desligado
	// travaria no dial. A tarefa tem de rodar mesmo sem ninguém observando.
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithTimeout(exportTimeout),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("criando exportador OTLP: %w", err)
	}

	// O recurso é montado À MÃO, campo a campo, e isso é decisão de segurança.
	//
	// Os detectores automáticos do SDK recolhem `process.command_args` — a linha
	// de comando inteira. O `main.go` já trata isso como invariante ("nunca de
	// argumento: `ps` mostra a linha de comando de qualquer processo"), e um
	// detector ligado por padrão furaria essa garantia mandando a `argv` para
	// fora da máquina, que é justamente o que telemetria faz.
	attributes := buildResource(serviceName, serviceVersion)

	provider := sdktrace.NewTracerProvider(
		// Em lote, nunca síncrono: exportar span a span faria cada ferramenta
		// esperar uma ida à rede antes de devolver o resultado ao modelo.
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(exportTimeout),
			sdktrace.WithExportTimeout(exportTimeout),
			// Fila modesta de propósito: a máquina tem ~2,9 GB livres e cada
			// tela custa ~500 MB de Chrome. Guardar dezenas de milhares de
			// trechos para um Mac que pode estar fechado é gastar a memória do
			// trabalho para observar o trabalho. Cheia, o SDK descarta o mais
			// novo — perda aceita, e preferível a competir com o Chrome.
			sdktrace.WithMaxQueueSize(2048),
		),
		sdktrace.WithResource(attributes),
	)

	// O provider NÃO é registrado como global (otel.SetTracerProvider).
	//
	// Global é estado compartilhado de processo: dois testes que montam agentes
	// diferentes passariam a escrever no mesmo lugar, e a ordem entre eles
	// mudaria o resultado. O agente recebe o rastreador por opção, como recebe
	// todas as outras dependências.
	return &Tracer{tracer: provider.Tracer(serviceName)}, provider.Shutdown, nil
}

// buildResource monta a identidade do serviço, campo a campo.
//
// À MÃO, e isso é decisão de segurança, não estilo. Os detectores automáticos
// do SDK recolhem `process.command_args` — a linha de comando inteira. O
// `main.go` já trata isso como invariante ("nunca de argumento: `ps` mostra a
// linha de comando de qualquer processo"), e um detector ligado por padrão
// furaria essa garantia mandando a `argv` para fora da máquina, que é
// exatamente o que telemetria faz.
//
// Compartilhada entre o rastreador e o medidor: dois recursos construídos
// separadamente divergem no dia em que alguém mexe num só, e o sintoma seria
// trace e métrica aparecendo como serviços diferentes no backend.
func buildResource(serviceName, serviceVersion string) *resource.Resource {
	return resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	)
}

// Start abre um trecho e devolve o contexto que o carrega.
func (t *Tracer) Start(ctx context.Context, name string, attributes ...service.Attribute) (context.Context, service.Span) {
	ctx, span := t.tracer.Start(ctx, name, oteltrace.WithAttributes(convert(attributes)...))
	return ctx, &spanAdapter{span: span}
}

// AddEvent marca um instante no trecho que o contexto carrega.
//
// `SpanFromContext` devolve um trecho não-gravante quando não há nenhum no
// contexto, e `AddEvent` nele é no-op. Por isso não há checagem aqui: o caminho
// "sem trecho" já é silencioso por construção do SDK.
func (t *Tracer) AddEvent(ctx context.Context, name string, attributes ...service.Attribute) {
	oteltrace.SpanFromContext(ctx).AddEvent(name, oteltrace.WithAttributes(convert(attributes)...))
}

// TraceContext devolve os identificadores do trecho que o contexto carrega.
//
// Só devolve algo quando o contexto do trecho é VÁLIDO. Um `SpanContext` vazio
// tem ids compostos só de zeros, e escrevê-los no log seria pior que não
// escrever nada: quem lesse acharia que há um trace a procurar, e não há.
func (t *Tracer) TraceContext(ctx context.Context) (string, string) {
	spanContext := oteltrace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return "", ""
	}
	return spanContext.TraceID().String(), spanContext.SpanID().String()
}

// spanAdapter adapta um trecho do OpenTelemetry ao porto do serviço.
type spanAdapter struct {
	span oteltrace.Span
}

// SetAttributes acrescenta atributos ao trecho já aberto.
func (s *spanAdapter) SetAttributes(attributes ...service.Attribute) {
	s.span.SetAttributes(convert(attributes)...)
}

// AddEvent marca um instante dentro do trecho.
func (s *spanAdapter) AddEvent(name string, attributes ...service.Attribute) {
	s.span.AddEvent(name, oteltrace.WithAttributes(convert(attributes)...))
}

// End fecha o trecho, marcando-o como falho quando houve erro.
//
// `RecordError` e `SetStatus` são coisas diferentes e as duas importam: o
// primeiro guarda a mensagem, o segundo é o que faz o trecho aparecer em
// vermelho e entrar na taxa de erro. Só um dos dois deixa metade do sinal fora.
func (s *spanAdapter) End(err error) {
	if err != nil {
		s.span.RecordError(err)
		s.span.SetStatus(codes.Error, err.Error())
	}
	s.span.End()
}

// convert traduz os atributos do porto para os tipos do OpenTelemetry.
//
// O tipo desconhecido vira texto em vez de ser descartado: perder um atributo em
// silêncio produz um trecho que parece completo e não é, e o defeito só aparece
// quando alguém procurar o campo e não achar.
func convert(attributes []service.Attribute) []attribute.KeyValue {
	converted := make([]attribute.KeyValue, 0, len(attributes))
	for _, item := range attributes {
		switch value := item.Value.(type) {
		case string:
			converted = append(converted, attribute.String(item.Key, value))
		case int:
			converted = append(converted, attribute.Int(item.Key, value))
		case int64:
			converted = append(converted, attribute.Int64(item.Key, value))
		case float64:
			converted = append(converted, attribute.Float64(item.Key, value))
		case bool:
			converted = append(converted, attribute.Bool(item.Key, value))
		default:
			converted = append(converted, attribute.String(item.Key, fmt.Sprint(value)))
		}
	}
	return converted
}

// ShutdownTimeout devolve o teto de encerramento, para quem monta o contexto.
func ShutdownTimeout() time.Duration { return shutdownTimeout }
