package service

import (
	"context"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// measurement é uma medida registrada pelo espião.
type measurement struct {
	name       string
	intValue   int64
	floatValue float64
	attributes map[string]any
}

// spyMeter guarda o que foi medido, para o teste afirmar sobre o conjunto.
type spyMeter struct {
	counts    []measurement
	floats    []measurement
	durations []measurement
	upDowns   []measurement
}

// asMap achata os atributos, para a asserção não depender da ordem.
func asMap(attributes []Attribute) map[string]any {
	result := make(map[string]any, len(attributes))
	for _, item := range attributes {
		result[item.Key] = item.Value
	}
	return result
}

// AddCount guarda a contagem.
func (s *spyMeter) AddCount(_ context.Context, name string, value int64, attributes ...Attribute) {
	s.counts = append(s.counts, measurement{name: name, intValue: value, attributes: asMap(attributes)})
}

// AddFloat guarda o valor fracionário.
func (s *spyMeter) AddFloat(_ context.Context, name string, value float64, attributes ...Attribute) {
	s.floats = append(s.floats, measurement{name: name, floatValue: value, attributes: asMap(attributes)})
}

// RecordDuration guarda a duração.
func (s *spyMeter) RecordDuration(_ context.Context, name string, seconds float64, attributes ...Attribute) {
	s.durations = append(s.durations, measurement{name: name, floatValue: seconds, attributes: asMap(attributes)})
}

// AddUpDown guarda a variação.
func (s *spyMeter) AddUpDown(_ context.Context, name string, value int64, attributes ...Attribute) {
	s.upDowns = append(s.upDowns, measurement{name: name, intValue: value, attributes: asMap(attributes)})
}

// find devolve a primeira medida com o nome dado.
func find(items []measurement, name string) (measurement, bool) {
	for _, item := range items {
		if item.name == name {
			return item, true
		}
	}
	return measurement{}, false
}

// TestDiscardMeterDoesNotPanic exercita o medidor mudo.
//
// É o caminho de TODA execução sem telemetria configurada — o padrão da
// máquina. Um pânico aqui derrubaria a tarefa por causa da observação.
func TestDiscardMeterDoesNotPanic(t *testing.T) {
	meter := discardMeter{}
	ctx := context.Background()
	meter.AddCount(ctx, "x", 1, String("k", "v"))
	meter.AddFloat(ctx, "x", 1.5)
	meter.RecordDuration(ctx, "x", 0.1)
	meter.AddUpDown(ctx, "x", -1)
}

// TestWithMeterIgnoresNil prova que nil não desliga o medidor existente.
//
// É o caminho real: quando o adaptador falha ao subir, ele devolve nil e a
// opção é chamada assim mesmo. Se nil sobrescrevesse, a primeira medição do
// laço entraria em pânico — exatamente quando a telemetria já tinha falhado.
func TestWithMeterIgnoresNil(t *testing.T) {
	agent := &Agent{meter: discardMeter{}}
	WithMeter(nil)(agent)
	if agent.meter == nil {
		t.Fatal("nil sobrescreveu o medidor; o laço entraria em pânico")
	}
	spy := &spyMeter{}
	WithMeter(spy)(agent)
	if agent.meter != spy {
		t.Fatal("o medidor informado não foi aplicado")
	}
}

// TestLoopMeasuresTokensCostAndDurations prova que uma tarefa produz o conjunto
// de medidas esperado.
//
// Falha se qualquer instrumento for removido — que é o critério do TEST-MAP: um
// teste só cobre uma funcionalidade se reprovar quando ela sumir.
func TestLoopMeasuresTokensCostAndDurations(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{
		{
			ToolCalls:        []domain.ToolCall{{ID: "c1", Name: "eco", Arguments: `{}`}},
			StopReason:       "tool_calls",
			PromptTokens:     100,
			CompletionTokens: 20,
			CachedTokens:     80,
		},
		{Content: "pronto", StopReason: "stop"},
	}}
	tool := &fakeTool{name: "eco", result: ports.ToolResult{Output: "ok"}}
	spy := &spyMeter{}
	agent := NewAgent(model, []ports.Tool{tool}, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithMeter(spy))

	task, err := domain.NewTask("task-metrics", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	if _, ok := find(spy.counts, metricTokens); !ok {
		t.Errorf("nenhum token medido; o custo do mês deixa de existir\n  %v", spy.counts)
	}
	if _, ok := find(spy.durations, metricTurnDuration); !ok {
		t.Errorf("duração do turno não medida\n  %v", spy.durations)
	}
	if _, ok := find(spy.durations, metricToolDuration); !ok {
		t.Errorf("duração da FERRAMENTA não medida — é o número que não existia\n  %v", spy.durations)
	}
	if _, ok := find(spy.counts, metricTaskOutcomes); !ok {
		t.Errorf("desfecho da tarefa não contado\n  %v", spy.counts)
	}
	if _, ok := find(spy.upDowns, metricTasksRunning); !ok {
		t.Errorf("tarefas em voo não medidas\n  %v", spy.upDowns)
	}
}

// TestTokenTypesAreSeparated prova que os três tipos viram séries distintas.
//
// Somá-los num número só perderia justamente a informação que importa: o cache
// custa 4× menos, e uma conta que não o separasse superestimaria o gasto na
// mesma proporção.
func TestTokenTypesAreSeparated(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{
		{Content: "pronto", StopReason: "stop", PromptTokens: 100, CompletionTokens: 20, CachedTokens: 80},
	}}
	spy := &spyMeter{}
	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithMeter(spy))

	task, err := domain.NewTask("task-token-types", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	byType := map[string]int64{}
	for _, item := range spy.counts {
		if item.name != metricTokens {
			continue
		}
		kind, _ := item.attributes[attrTokenType].(string)
		byType[kind] = item.intValue
	}
	want := map[string]int64{tokenTypeInput: 100, tokenTypeOutput: 20, tokenTypeCached: 80}
	for kind, expected := range want {
		if byType[kind] != expected {
			t.Errorf("token %q: got %d, want %d", kind, byType[kind], expected)
		}
	}
}

// TestRunningGaugeReturnsToZero prova que o medidor sobe e DESCE.
//
// Sem o decremento, a linha subiria para sempre e responderia "quantas tarefas
// já rodaram", não "quantas rodam agora" — e o painel mostraria uma máquina
// permanentemente lotada.
func TestRunningGaugeReturnsToZero(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	spy := &spyMeter{}
	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithMeter(spy))

	task, err := domain.NewTask("task-gauge", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	var total int64
	for _, item := range spy.upDowns {
		if item.name == metricTasksRunning {
			total += item.intValue
		}
	}
	if total != 0 {
		t.Errorf("o medidor não voltou a zero: soma %d — a linha sobe para sempre", total)
	}
}

// TestNoMetricLabelCarriesTaskID é o teste de CARDINALIDADE.
//
// É o defeito mais caro possível num backend de métricas, e o mais fácil de
// cometer: o id da tarefa é `task-<UnixNano>`, único por execução, e como
// rótulo criaria uma série NOVA a cada tarefa. Séries não são apagadas, então o
// custo é permanente e cresce para sempre.
//
// O id vai no TRECHO, onde é barato. Nunca aqui.
func TestNoMetricLabelCarriesTaskID(t *testing.T) {
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	spy := &spyMeter{}
	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithMeter(spy))

	const taskID = "task-1788000000000000000"
	task, err := domain.NewTask(taskID, 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	groups := [][]measurement{spy.counts, spy.floats, spy.durations, spy.upDowns}
	for _, group := range groups {
		for _, item := range group {
			for key, value := range item.attributes {
				text, isText := value.(string)
				if !isText {
					continue
				}
				if strings.Contains(text, taskID) || strings.HasPrefix(text, "task-") {
					t.Errorf("o rótulo %q de %q carrega o id da tarefa (%q) — cardinalidade ilimitada",
						key, item.name, text)
				}
			}
		}
	}
}

// TestGuardrailHitIsCounted prova que o bloqueio vira número.
//
// O trecho registra o guardrail de UMA tarefa; só a métrica responde "qual
// detector vem disparando", que é a pergunta de quem quer ajustar o teto.
func TestGuardrailHitIsCounted(t *testing.T) {
	// Um turno que estoura o teto de turnos: a tarefa já nasce no limite.
	model := &fakeModel{responses: []ports.Completion{{Content: "pronto", StopReason: "stop"}}}
	spy := &spyMeter{}
	agent := NewAgent(model, nil, &fakeScreen{}, newFakeStore(), &fakeLock{},
		fixedClock, "instruções", WithMeter(spy))

	task, err := domain.NewTask("task-guardrail", 1, "faça algo", fixedClock())
	if err != nil {
		t.Fatalf("montando a tarefa: %v", err)
	}
	// Acima do teto acumulado, para o detector de turnos morder na primeira volta.
	task.TurnsUsed = turnCap + 1

	if err := agent.Run(context.Background(), task); err != nil {
		t.Fatalf("a tarefa falhou: %v", err)
	}

	hit, ok := find(spy.counts, metricGuardrailHits)
	if !ok {
		t.Fatalf("o guardrail não foi contado\n  %v", spy.counts)
	}
	if kind, _ := hit.attributes[attrGuardrailKind].(string); kind != string(GuardrailTurnCap) {
		t.Errorf("tipo do detector: got %q, want %q", kind, GuardrailTurnCap)
	}
}
