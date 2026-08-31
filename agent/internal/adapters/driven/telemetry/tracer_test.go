package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// TestNewWithoutEndpointIsNotAnError prova que a ausência de endpoint é caso normal.
//
// É o estado padrão da máquina: sem `AGENTD_OTLP_ENDPOINT`, o agente tem de
// rodar igual. Tratar isso como erro faria o `buildDeps` imprimir aviso em toda
// execução e treinaria quem opera a ignorar o aviso — inclusive quando ele fosse
// de um defeito real.
func TestNewWithoutEndpointIsNotAnError(t *testing.T) {
	tracer, shutdown, err := New(context.Background(), "", "agentd", "0.0.0")
	if err != nil {
		t.Fatalf("endpoint vazio devolveu erro: %v", err)
	}
	if tracer != nil {
		t.Fatal("sem endpoint o rastreador tem que ser nil, para o serviço usar o mudo dele")
	}
	if shutdown == nil {
		t.Fatal("o encerrador nunca pode ser nil: quem chama não checa antes de usar")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("o encerrador no-op devolveu erro: %v", err)
	}
}

// TestConvertMapsEachType cobre um ramo por vez da conversão de atributos.
//
// Cada linha existe porque o tipo errado no backend é falha SILENCIOSA: um
// número exportado como texto some de qualquer agregação sem produzir erro, e o
// painel fica vazio com todo mundo achando que o dado não foi gerado.
func TestConvertMapsEachType(t *testing.T) {
	cases := []struct {
		name  string
		input service.Attribute
		want  attribute.KeyValue
	}{
		{"texto", service.String("s", "abc"), attribute.String("s", "abc")},
		{"inteiro", service.Int("i", 7), attribute.Int("i", 7)},
		{"inteiro de 64 bits", service.Int64("i64", 8), attribute.Int64("i64", 8)},
		{"fracionário", service.Float64("f", 1.5), attribute.Float64("f", 1.5)},
		{"booleano", service.Bool("b", true), attribute.Bool("b", true)},
		// O ramo padrão: tipo que não conhecemos vira texto em vez de sumir.
		// Perder o atributo em silêncio produziria um trecho que parece completo
		// e não é — o pior desfecho, porque ninguém vai procurar o que falta.
		{"desconhecido vira texto", service.Attribute{Key: "d", Value: time.Second}, attribute.String("d", "1s")},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := convert([]service.Attribute{testCase.input})
			if len(got) != 1 {
				t.Fatalf("esperava 1 atributo, veio %d", len(got))
			}
			if got[0] != testCase.want {
				t.Errorf("got %#v, want %#v", got[0], testCase.want)
			}
		})
	}
}

// TestConvertEmptyList confere que nenhuma entrada produz nenhuma saída.
func TestConvertEmptyList(t *testing.T) {
	if got := convert(nil); len(got) != 0 {
		t.Fatalf("lista vazia produziu %d atributos", len(got))
	}
}

// newRecordingTracer monta um rastreador que guarda os trechos em memória.
//
// Sem rede e sem backend: o que se quer verificar é o que o adaptador PRODUZ, e
// um coletor de verdade no meio só acrescentaria uma fonte de intermitência.
func newRecordingTracer(t *testing.T) (*Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	return &Tracer{tracer: provider.Tracer("teste")}, recorder
}

// TestSpanRecordsAttributesAndEvents prova que atributo de abertura, atributo
// posterior e evento chegam todos ao trecho exportado.
//
// Os três caminhos importam por motivos diferentes: o de abertura carrega a
// identidade da tarefa, o posterior carrega tokens e custo (que só se conhecem
// depois da resposta), e o evento é como guardrail e take-over aparecem.
func TestSpanRecordsAttributesAndEvents(t *testing.T) {
	tracer, recorder := newRecordingTracer(t)

	_, span := tracer.Start(context.Background(), "agentd.task", service.String("id", "task-1"))
	span.SetAttributes(service.Int("turns", 3))
	span.AddEvent("agentd.guardrail.hit", service.String("kind", "custo"))
	span.End(nil)

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("esperava 1 trecho, veio %d", len(ended))
	}
	recorded := ended[0]

	if recorded.Name() != "agentd.task" {
		t.Errorf("nome: got %q, want %q", recorded.Name(), "agentd.task")
	}
	if got := attributeValue(recorded.Attributes(), "id"); got != "task-1" {
		t.Errorf("atributo de abertura perdido: got %q", got)
	}
	if got := attributeValue(recorded.Attributes(), "turns"); got != "3" {
		t.Errorf("atributo posterior perdido: got %q", got)
	}
	if len(recorded.Events()) != 1 || recorded.Events()[0].Name != "agentd.guardrail.hit" {
		t.Errorf("evento perdido: %#v", recorded.Events())
	}
	// Sem erro, o trecho não pode aparecer como falho: senão toda tarefa bem
	// sucedida entraria na taxa de erro e o alerta viveria disparado.
	if recorded.Status().Code == codes.Error {
		t.Error("trecho sem erro foi marcado como falho")
	}
}

// TestSpanWithErrorMarksFailureAndKeepsMessage prova que End(err) faz as DUAS
// coisas.
//
// São separadas e as duas importam: `RecordError` guarda a mensagem para quem
// for diagnosticar, e `SetStatus` é o que faz o trecho contar como erro na taxa.
// Fazer só uma deixa metade do sinal de fora — e a metade que falta é sempre a
// que alguém procura primeiro.
func TestSpanWithErrorMarksFailureAndKeepsMessage(t *testing.T) {
	tracer, recorder := newRecordingTracer(t)

	_, span := tracer.Start(context.Background(), "agentd.task")
	span.End(errors.New("tela ocupada"))

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("esperava 1 trecho, veio %d", len(ended))
	}
	recorded := ended[0]

	if recorded.Status().Code != codes.Error {
		t.Error("o trecho não foi marcado como falho; ele sumiria da taxa de erro")
	}
	if recorded.Status().Description != "tela ocupada" {
		t.Errorf("descrição: got %q", recorded.Status().Description)
	}
	if len(recorded.Events()) == 0 {
		t.Error("a mensagem do erro não foi registrada como evento")
	}
}

// TestAddEventAttachesToContextSpan prova que o evento vai para o trecho
// que o contexto carrega, sem ninguém passar o trecho por parâmetro.
//
// É como guardrail e take-over são registrados: `applyHit` é chamado de sete
// lugares no laço, e nenhum deles tem o trecho à mão.
func TestAddEventAttachesToContextSpan(t *testing.T) {
	tracer, recorder := newRecordingTracer(t)

	ctx, span := tracer.Start(context.Background(), "agentd.task")
	tracer.AddEvent(ctx, "agentd.guardrail.hit", service.String("kind", "custo"))
	span.End(nil)

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("esperava 1 trecho, veio %d", len(ended))
	}
	events := ended[0].Events()
	if len(events) != 1 || events[0].Name != "agentd.guardrail.hit" {
		t.Fatalf("o evento não chegou ao trecho do contexto: %#v", events)
	}
	if got := attributeValue(events[0].Attributes, "kind"); got != "custo" {
		t.Errorf("atributo do evento perdido: got %q", got)
	}
}

// TestAddEventWithoutSpanDoesNotPanic cobre o caminho do CLI de operação
// local, que roda sem nenhum trecho aberto.
//
// Um pânico aqui derrubaria justamente `agentd -abandon`, que é o comando usado
// para destravar uma tela quando algo já deu errado.
func TestAddEventWithoutSpanDoesNotPanic(t *testing.T) {
	tracer, _ := newRecordingTracer(t)
	tracer.AddEvent(context.Background(), "agentd.guardrail.hit")
}

// TestTraceContextReturnsIdentifiers prova o elo entre o arquivo e o backend.
//
// É o que carimba o `activity.log`: quem estiver lendo o arquivo por SSH copia
// o id e abre a cascata inteira, sem correlacionar por horário — que é o método
// que erra quando duas tarefas rodam em telas diferentes ao mesmo tempo.
func TestTraceContextReturnsIdentifiers(t *testing.T) {
	tracer, _ := newRecordingTracer(t)

	ctx, span := tracer.Start(context.Background(), "agentd.task")
	traceID, spanID := tracer.TraceContext(ctx)
	span.End(nil)

	// 32 e 16 dígitos hexadecimais: os tamanhos do padrão. Um id truncado em
	// algum ponto do caminho não casaria com o do backend, e o elo quebraria
	// sem que nada falhasse.
	if len(traceID) != 32 {
		t.Errorf("trace_id tem %d caracteres, esperados 32: %q", len(traceID), traceID)
	}
	if len(spanID) != 16 {
		t.Errorf("span_id tem %d caracteres, esperados 16: %q", len(spanID), spanID)
	}
}

// TestTraceContextWithoutSpanReturnsEmpty prova que o carimbo some sem trace.
//
// É o caso da máquina sem telemetria — o padrão. Devolver ids compostos só de
// zeros seria pior que devolver vazio: quem lesse a linha acharia que há um
// trace a procurar, e não há.
func TestTraceContextWithoutSpanReturnsEmpty(t *testing.T) {
	tracer, _ := newRecordingTracer(t)

	traceID, spanID := tracer.TraceContext(context.Background())
	if traceID != "" || spanID != "" {
		t.Errorf("sem trecho deveria devolver vazio, veio %q / %q", traceID, spanID)
	}
}

// TestStartPropagatesContext prova que o contexto devolvido carrega o trecho.
//
// É o que faz um trecho filho se pendurar no pai. Sem isso cada trecho viraria
// um trace solto, e a cascata que a tarefa deveria desenhar sairia como dezenas
// de linhas sem relação nenhuma.
func TestStartPropagatesContext(t *testing.T) {
	tracer, recorder := newRecordingTracer(t)

	ctx, parent := tracer.Start(context.Background(), "pai")
	_, child := tracer.Start(ctx, "filho")
	child.End(nil)
	parent.End(nil)

	ended := recorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("esperava 2 trechos, veio %d", len(ended))
	}
	// O primeiro a fechar é o filho.
	if ended[0].Parent().SpanID() != ended[1].SpanContext().SpanID() {
		t.Error("o filho não ficou pendurado no pai; a cascata não se forma")
	}
}

// attributeValue devolve o valor de uma chave como texto, ou vazio se ausente.
func attributeValue(attributes []attribute.KeyValue, key string) string {
	for _, item := range attributes {
		if string(item.Key) == key {
			return item.Value.Emit()
		}
	}
	return ""
}

// TestShutdownTimeoutIsBelowSystemdGrace trava o número contra mudança
// distraída.
//
// A unidade concede 40s antes do SIGKILL, e esses 40s são das TAREFAS, para
// gravarem estado e soltarem a trava. Se alguém aumentar este teto para perto
// daquele, telemetria para um Mac desligado passa a consumir o orçamento de quem
// tem trabalho real a salvar — e o sintoma seria tarefa perdendo estado no
// restart, que ninguém ligaria a telemetria.
func TestShutdownTimeoutIsBelowSystemdGrace(t *testing.T) {
	const systemdStopTimeout = 40 * time.Second
	if ShutdownTimeout() >= systemdStopTimeout/2 {
		t.Fatalf("teto de flush %v é grande demais perto dos %v do systemd",
			ShutdownTimeout(), systemdStopTimeout)
	}
}
