package service

import "context"

// Meter registra medidas agregáveis do agente.
//
// Porto LOCAL, pelo mesmo critério do Tracer: `ports/` descreve o que o agente
// EXIGE para funcionar, e ele funciona inteiro sem métrica nenhuma.
//
// A diferença entre isto e o Tracer não é de implementação, é de PERGUNTA. O
// trecho responde "o que aconteceu nesta tarefa"; a métrica responde "como as
// tarefas vêm se comportando". Um trace guarda tudo de uma execução e é caro de
// agregar; uma métrica guarda pouco de todas e é barata de agregar. Quem tenta
// responder a segunda pergunta somando traces descobre tarde que o backend não
// foi feito para isso.
type Meter interface {
	// AddCount soma a um contador monotônico.
	//
	// Para o que só cresce: tokens gastos, tarefas encerradas, guardrails
	// disparados. O backend deriva a taxa a partir do acumulado, e por isso
	// perder uma amostra não perde o total — que é a propriedade que distingue
	// contador de medidor.
	AddCount(ctx context.Context, name string, value int64, attributes ...Attribute)

	// AddFloat soma a um contador monotônico de valor fracionário.
	//
	// Existe separado porque dinheiro não cabe em inteiro sem escolher uma
	// unidade arbitrária, e "custo em micro-dólares" é o tipo de decisão que
	// alguém interpreta errado seis meses depois.
	AddFloat(ctx context.Context, name string, value float64, attributes ...Attribute)

	// RecordDuration guarda uma duração num histograma, em segundos.
	//
	// Histograma e não média: a média de latência esconde exatamente o que
	// interessa. Uma tarefa de 40 s entre noventa de 2 s desaparece numa média
	// de 2,4 s, e é ela que alguém está tentando explicar.
	RecordDuration(ctx context.Context, name string, seconds float64, attributes ...Attribute)

	// AddUpDown soma a um contador que sobe e desce.
	//
	// Para o que tem valor CORRENTE: tarefas em execução, telas ocupadas.
	// Usar contador monotônico aqui produziria uma linha sempre subindo, que
	// não responde "quantas rodam agora".
	AddUpDown(ctx context.Context, name string, value int64, attributes ...Attribute)
}

// Nomes dos instrumentos e dos rótulos.
//
// 🛑 REGRA DE CARDINALIDADE, e ela é a diferença entre um backend saudável e um
// inutilizável: NENHUM rótulo aqui pode ter valor ilimitado.
//
// `task.ID` é `task-<UnixNano>`, único por execução. Como rótulo, ele criaria
// uma série de métrica POR TAREFA — e séries não são apagadas, então o custo
// seria permanente e cresceria para sempre. Ele vai no TRECHO, onde é barato, e
// nunca aqui.
//
// Todos os rótulos abaixo são de conjunto fechado e pequeno: nome de modelo,
// nome de ferramenta, tipo de token, motivo de bloqueio, número de tela (1..9).
const (
	// metricTokens conta tokens consumidos, separados por tipo.
	metricTokens = "agentd.model.tokens"

	// metricCostUSD acumula o gasto de inferência em dólares.
	//
	// É a métrica que responde à observação que o README já registra: em agente
	// autônomo a inferência custa mais que o servidor. Sem ela, o número só
	// existe dentro de cada tarefa e nunca vira total do mês.
	metricCostUSD = "agentd.model.cost.usd"

	// metricTurnDuration é o histograma do tempo de um turno do modelo.
	metricTurnDuration = "agentd.turn.duration"

	// metricToolDuration é o histograma do tempo de UMA ferramenta.
	//
	// É o instrumento que não existia: até agora nenhuma ferramenta era
	// cronometrada, e "a tarefa demorou" não se decompunha em modelo, navegador
	// e shell.
	metricToolDuration = "agentd.tool.duration"

	// metricGuardrailHits conta bloqueios por detector.
	metricGuardrailHits = "agentd.guardrail.hits"

	// metricTaskOutcomes conta desfechos de tarefa.
	//
	// Com o rótulo de estado, ela responde de uma vez a pergunta que motivou
	// este trabalho: 31 de 123 tarefas terminaram `blocked`, e ninguém sabia.
	metricTaskOutcomes = "agentd.task.outcomes"

	// metricTasksRunning é quantas tarefas rodam AGORA.
	metricTasksRunning = "agentd.tasks.running"

	// Rótulos. Todos de conjunto fechado — ver a regra de cardinalidade acima.
	attrModel     = "agentd.model"
	attrTokenType = "agentd.token.type"
	attrStopReason = "agentd.stop_reason"
	attrToolName  = "agentd.tool.name"
	attrTaskState = "agentd.task.state"

	// Valores do rótulo de tipo de token.
	tokenTypeInput  = "input"
	tokenTypeOutput = "output"
	tokenTypeCached = "cached"
)

// discardMeter é o medidor padrão: não registra nada.
//
// Mesmo motivo do discardTracer — sem ele, cada ponto de medição precisaria de
// uma checagem de nil, e a que faltasse viraria pânico dentro do laço.
type discardMeter struct{}

// AddCount descarta a contagem.
func (discardMeter) AddCount(context.Context, string, int64, ...Attribute) {}

// AddFloat descarta o valor fracionário.
func (discardMeter) AddFloat(context.Context, string, float64, ...Attribute) {}

// RecordDuration descarta a duração.
func (discardMeter) RecordDuration(context.Context, string, float64, ...Attribute) {}

// AddUpDown descarta a variação.
func (discardMeter) AddUpDown(context.Context, string, int64, ...Attribute) {}

// WithMeter liga o agente a um medidor.
//
// Nil é ignorado, como nas outras opções: um adaptador que falhou ao subir
// devolve nil, e nesse caso o certo é o agente seguir mudo em vez de não rodar.
func WithMeter(meter Meter) Option {
	return func(a *Agent) {
		if meter != nil {
			a.meter = meter
		}
	}
}
