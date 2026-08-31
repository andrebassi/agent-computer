package service

import (
	"context"
	"errors"
	"testing"
)

// TestDiscardTracerReturnsContextUnchanged prova que o rastreador mudo não mexe
// no contexto que recebeu.
//
// Importa porque o contexto carrega prazo e cancelamento da tarefa: um
// rastreador que o substituísse por outro faria o cancelamento parar de chegar
// nas camadas de baixo, e o sintoma seria uma tarefa que ignora Ctrl+C — bem
// longe de qualquer suspeita de telemetria.
func TestDiscardTracerReturnsContextUnchanged(t *testing.T) {
	type contextKey string
	const marker contextKey = "marker"

	ctx := context.WithValue(context.Background(), marker, "presente")
	returned, span := discardTracer{}.Start(ctx, "qualquer", String("k", "v"))

	if returned.Value(marker) != "presente" {
		t.Fatal("o contexto devolvido perdeu o valor que carregava")
	}
	if span == nil {
		t.Fatal("o trecho devolvido não pode ser nil: quem chama sempre o fecha")
	}
}

// TestDiscardSpanDoesNotPanic exercita os três métodos do trecho mudo.
//
// É o caminho que roda em TODA execução sem telemetria configurada — ou seja, o
// padrão da máquina. Um pânico aqui derrubaria a tarefa por causa da observação
// que ninguém pediu.
func TestDiscardSpanDoesNotPanic(t *testing.T) {
	_, span := discardTracer{}.Start(context.Background(), "tarefa")
	span.SetAttributes(Int("n", 1))
	span.AddEvent("algo", Bool("ok", true))
	span.End(nil)
	span.End(errors.New("também com erro"))
}

// TestAttributeConstructors confere que cada construtor guarda chave e valor com
// o tipo original.
//
// O tipo importa porque o adaptador decide a conversão por ele: um valor que
// chegasse como outro tipo cairia no ramo padrão e viraria texto, e um número
// exportado como texto não entra em cálculo nenhum no backend — o gráfico fica
// vazio sem nenhum erro aparecer.
func TestAttributeConstructors(t *testing.T) {
	cases := []struct {
		name      string
		attribute Attribute
		wantKey   string
		wantValue any
	}{
		{"texto", String("s", "abc"), "s", "abc"},
		{"inteiro", Int("i", 7), "i", 7},
		{"inteiro de 64 bits", Int64("i64", int64(8)), "i64", int64(8)},
		{"fracionário", Float64("f", 1.5), "f", 1.5},
		{"booleano", Bool("b", true), "b", true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.attribute.Key != testCase.wantKey {
				t.Errorf("chave: got %q, want %q", testCase.attribute.Key, testCase.wantKey)
			}
			if testCase.attribute.Value != testCase.wantValue {
				t.Errorf("valor: got %#v, want %#v", testCase.attribute.Value, testCase.wantValue)
			}
		})
	}
}

// Identificadores fixos do espião. Valores com a forma real — 32 e 16 dígitos
// hexadecimais — para o teste falhar se alguém truncar o campo em algum ponto.
const (
	spyTraceID = "0af7651916cd43dd8448eb211c80319c"
	spySpanID  = "b7ad6b7169203331"
)

// spyTracer registra o que foi observado, para os testes do laço.
type spyTracer struct {
	started []string
	events  []string
}

// Start guarda o nome do trecho e devolve um espião de trecho.
func (s *spyTracer) Start(ctx context.Context, name string, _ ...Attribute) (context.Context, Span) {
	s.started = append(s.started, name)
	return ctx, &spySpan{}
}

// AddEvent guarda o nome do instante marcado.
func (s *spyTracer) AddEvent(_ context.Context, name string, _ ...Attribute) {
	s.events = append(s.events, name)
}

// TraceContext devolve identificadores fixos, para o teste poder afirmar que
// eles chegaram ao diário sem depender de um valor aleatório.
func (s *spyTracer) TraceContext(context.Context) (string, string) {
	return spyTraceID, spySpanID
}

// spySpan é o trecho do espião: conta os fechamentos e guarda o erro.
type spySpan struct {
	ended int
	err   error
}

// SetAttributes ignora os atributos — o espião só verifica o percurso.
func (s *spySpan) SetAttributes(...Attribute) {}

// AddEvent ignora o instante marcado.
func (s *spySpan) AddEvent(string, ...Attribute) {}

// End conta o fechamento e guarda o erro recebido.
func (s *spySpan) End(err error) {
	s.ended++
	s.err = err
}

// TestWithTracerIgnoresNil prova que passar nil não desliga o rastreador que já
// estava lá.
//
// É o caminho REAL de produção: quando o adaptador falha ao subir, ele devolve
// nil, e a opção é chamada mesmo assim. Se nil sobrescrevesse, o agente ficaria
// com o campo nulo — e a primeira chamada do laço entraria em pânico, exatamente
// no cenário em que a telemetria já tinha falhado.
func TestWithTracerIgnoresNil(t *testing.T) {
	agent := &Agent{tracer: discardTracer{}}

	WithTracer(nil)(agent)
	if agent.tracer == nil {
		t.Fatal("nil sobrescreveu o rastreador; o laço entraria em pânico")
	}

	spy := &spyTracer{}
	WithTracer(spy)(agent)
	if agent.tracer != spy {
		t.Fatal("o rastreador informado não foi aplicado")
	}
}
