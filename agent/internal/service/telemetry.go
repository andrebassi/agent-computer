package service

import "context"

// Tracer abre e fecha trechos de trabalho para observação externa.
//
// É um porto LOCAL, definido aqui dentro e não em `ports/`, pelo mesmo critério
// que já vale para GuardrailJournal e CostEstimator: `ports/` descreve o que o
// agente exige do mundo para funcionar, e o agente funciona inteiro sem
// telemetria. Instrumentação cruzada e opcional mora junto de quem a usa.
//
// A interface é própria em vez de `go.opentelemetry.io/otel/trace.Tracer` por
// uma razão que o próprio repositório já registrou: `service` e `domain` não têm
// um único import de terceiro, e o comentário de guardrails.go documenta que o
// laço ficou sem logger POR DECISÃO. Trocar isso por um SDK inteiro no meio do
// laço desfaria a decisão sem que ninguém a tivesse reaberto. O SDK vive no
// adaptador, que é onde ele pode ser trocado sem tocar em regra de negócio.
type Tracer interface {
	// Start abre um trecho e devolve o contexto que o carrega, mais o trecho.
	//
	// O contexto devolvido é o que propaga a identidade do trace para baixo —
	// por isso quem chama PRECISA usá-lo daí em diante, e não o original.
	Start(ctx context.Context, name string, attributes ...Attribute) (context.Context, Span)

	// AddEvent marca um instante no trecho que o contexto carrega.
	//
	// Existe para que guardrail e take-over sejam registrados sem que o trecho
	// tenha de descer por parâmetro até eles. `applyHit` é chamado de sete
	// lugares no laço; passar um `Span` por todos eles poluiria sete assinaturas
	// para carregar algo que o `ctx` já leva.
	//
	// Sem trecho no contexto, não faz nada — que é o caso do CLI de operação
	// local, e não deveria ser erro.
	AddEvent(ctx context.Context, name string, attributes ...Attribute)

	// TraceContext devolve os identificadores do trecho que o contexto carrega.
	//
	// Existe para carimbar o `activity.log` com o mesmo `trace_id` do backend —
	// é o que transforma "esta linha de log" em "esta linha, e a cascata inteira
	// da tarefa que a produziu", sem quem lê ter de correlacionar por horário.
	//
	// Devolve vazio quando não há trecho, e nesse caso nada é acrescentado à
	// linha. O arquivo continua legível por `tail` numa máquina sem telemetria,
	// que é justamente quando ele é a única coisa que resta.
	TraceContext(ctx context.Context) (traceID string, spanID string)
}

// Span é um trecho de trabalho aberto, à espera de ser fechado.
type Span interface {
	// SetAttributes acrescenta informação descoberta depois da abertura.
	//
	// Existe porque tokens, custo e motivo de parada só se conhecem DEPOIS da
	// resposta do modelo — e reabrir um trecho para carimbá-los seria mentir
	// sobre a duração.
	SetAttributes(attributes ...Attribute)

	// AddEvent marca um instante dentro do trecho.
	//
	// É o que guardrail e take-over usam, em vez de trecho próprio: um trecho
	// que fica aberto até uma pessoa agir pode durar horas e distorce toda
	// estatística de duração em que ele entrar.
	AddEvent(name string, attributes ...Attribute)

	// End fecha o trecho. Erro não-nulo o marca como falho.
	End(err error)
}

// Nomes dos trechos e dos atributos, num lugar só.
//
// Ficam constantes porque nome de span e chave de atributo são CONTRATO com o
// backend: um painel, um alerta e uma consulta salva apontam para estas
// strings. Espalhá-las pelos pontos de instrumentação faz um erro de digitação
// virar série nova em vez de erro de compilação — e séries paralelas com nomes
// quase iguais são o modo silencioso de um painel parar de somar.
//
// Onde a convenção GenAI do OpenTelemetry já define o nome, ele é usado como
// está. Onde ela não cobre — tela, take-over, guardrail, CDP — o prefixo é
// `agentd.`, para deixar claro o que é padrão e o que é nosso.
const (
	// spanTask é a tarefa inteira, do pedido ao desfecho.
	spanTask = "agentd.task"

	// attrTaskID identifica a tarefa. Vai no trecho, NUNCA como rótulo de
	// métrica: o id é `task-<UnixNano>`, único por tarefa, e como rótulo criaria
	// uma série de métrica por execução.
	attrTaskID = "agentd.task.id"

	// attrScreen é a tela (1..9) em que a tarefa roda.
	attrScreen = "agentd.task.screen"

	// attrResumed distingue a primeira execução da retomada após take-over.
	//
	// É atributo em vez de nome de trecho separado porque as duas são a mesma
	// unidade de trabalho: separá-las por nome faria toda estatística de duração
	// de tarefa ignorar metade das execuções.
	attrResumed = "agentd.task.resumed"

	// eventTurn marca o fim de um turno, com o acumulado da tarefa.
	//
	// É evento e não trecho por uma razão prática: um trecho por iteração
	// exigiria reescrever o corpo do laço — são oito pontos de saída, e um
	// `defer` não fecha no lugar certo dentro de um `for`. O evento entrega o
	// que interessa (a curva de custo e de turnos ao longo da tarefa) sem essa
	// cirurgia, e a cascata continua legível com chat e ferramenta pendurados
	// direto na tarefa.
	eventTurn = "agentd.turn"

	// attrTurnsUsed é o contador ACUMULADO de turnos, que atravessa retomadas.
	//
	// Diferente de attrIteration: uma tarefa retomada começa a iteração de novo
	// em zero, mas os turnos continuam de onde pararam. É a diferença entre "a
	// quantas voltas estou nesta execução" e "quanto desta tarefa já foi gasto".
	attrTurnsUsed = "agentd.task.turns_used"

	// spanChat é a chamada ao modelo. O nome vira "chat <modelo>", como manda a
	// convenção GenAI do OpenTelemetry.
	spanChat = "chat"

	// spanExecuteTool é a execução de UMA ferramenta. Vira "execute_tool <nome>".
	spanExecuteTool = "execute_tool"

	// Atributos da convenção GenAI, usados com o nome que ela define.
	//
	// Vale a pena não inventar aqui: quem já tem painel de agente construído
	// sobre estes nomes enxerga este agente sem configurar nada.
	attrGenAIOperation    = "gen_ai.operation.name"
	attrGenAISystem       = "gen_ai.system"
	attrGenAIRequestModel = "gen_ai.request.model"
	attrGenAIFinishReason = "gen_ai.response.finish_reasons"
	attrGenAIInputTokens  = "gen_ai.usage.input_tokens"
	attrGenAIOutputTokens = "gen_ai.usage.output_tokens"
	attrGenAIToolName     = "gen_ai.tool.name"
	attrGenAIToolCallID   = "gen_ai.tool.call.id"

	// genAISystemXai identifica o fornecedor no atributo da convenção.
	genAISystemXai = "xai"

	// attrCachedTokens é a parcela do prompt que veio do cache do fornecedor.
	//
	// É atributo NOSSO porque a convenção GenAI não tem equivalente estável, e
	// aqui ele é caro de ignorar: o cache custa 4x menos, e uma conta que o
	// desprezasse superestimaria o gasto em 4x.
	attrCachedTokens = "agentd.tokens.cached"

	// attrCostUSD é o custo acumulado da tarefa em dólares.
	attrCostUSD = "agentd.cost.usd"

	// attrToolFailed diz se a ferramenta devolveu falha.
	//
	// Separado do status de erro do trecho de propósito: falha de ferramenta
	// NÃO derruba a tarefa (um `grep` sem resultado já devolve 1), então marcar
	// o trecho como erro poria toda tarefa saudável na taxa de erro.
	attrToolFailed = "agentd.tool.failed"

	// attrToolArgsHash identifica a chamada sem expor os argumentos.
	//
	// Reaproveita a chave que o detector de laço já usa. É o que permite ver
	// "esta é a mesma chamada que falhou três vezes" sem mandar para fora da
	// máquina o comando que o modelo escreveu — que pode conter qualquer coisa.
	attrToolArgsHash = "agentd.tool.args_hash"

	// attrToolUnknown marca chamada a ferramenta que não existe.
	attrToolUnknown = "agentd.tool.unknown"

	// eventGuardrailHit é o instante em que um detector conteve a tarefa.
	//
	// Evento, e não trecho: o guardrail é um ponto no tempo, não um intervalo de
	// trabalho. E fica pendurado no trecho aberto, que é onde alguém procurando
	// "por que esta tarefa parou" já está olhando.
	eventGuardrailHit = "agentd.guardrail.hit"

	// attrGuardrailKind é qual detector disparou. Conjunto fechado de cinco.
	attrGuardrailKind = "agentd.guardrail.kind"

	// eventTakeoverRequested é o instante em que a tarefa pediu uma pessoa.
	eventTakeoverRequested = "agentd.takeover.requested"

	// attrBlockReason é o motivo do bloqueio. Conjunto fechado de seis.
	attrBlockReason = "agentd.block.reason"
)

// Attribute é um par nome/valor pendurado num trecho.
//
// O valor é `any` de propósito: a conversão para os tipos que o OpenTelemetry
// aceita acontece no adaptador. Trazer os tipos dele para cá exigiria importá-lo
// aqui, que é exatamente o que este porto existe para evitar.
type Attribute struct {
	Key   string
	Value any
}

// String monta um atributo de texto.
func String(key, value string) Attribute { return Attribute{Key: key, Value: value} }

// Int monta um atributo numérico inteiro.
func Int(key string, value int) Attribute { return Attribute{Key: key, Value: value} }

// Int64 monta um atributo numérico inteiro de 64 bits.
func Int64(key string, value int64) Attribute { return Attribute{Key: key, Value: value} }

// Float64 monta um atributo numérico fracionário. É o do custo em dólares.
func Float64(key string, value float64) Attribute { return Attribute{Key: key, Value: value} }

// Bool monta um atributo booleano.
func Bool(key string, value bool) Attribute { return Attribute{Key: key, Value: value} }

// discardTracer é o rastreador padrão: não observa nada.
//
// Mora aqui pelo mesmo motivo do discardSink — o serviço depende de portos,
// nunca de adaptadores. E, como os outros dois padrões mudos, existe para que
// nenhum ponto de instrumentação precise checar nil: a checagem que faltasse
// viraria pânico dentro do laço, derrubando a tarefa por causa da telemetria,
// que é o oposto do que telemetria deve fazer.
type discardTracer struct{}

// Start devolve o contexto intacto e um trecho que ignora tudo.
func (discardTracer) Start(ctx context.Context, _ string, _ ...Attribute) (context.Context, Span) {
	return ctx, discardSpan{}
}

// AddEvent descarta o instante marcado.
func (discardTracer) AddEvent(context.Context, string, ...Attribute) {}

// TraceContext devolve vazio: sem rastreador não há trace a que se referir.
func (discardTracer) TraceContext(context.Context) (string, string) { return "", "" }

// discardSpan é o trecho que não registra nada.
type discardSpan struct{}

// SetAttributes descarta os atributos.
func (discardSpan) SetAttributes(...Attribute) {}

// AddEvent descarta o instante marcado.
func (discardSpan) AddEvent(string, ...Attribute) {}

// End fecha sem registrar nada, inclusive quando houve erro.
func (discardSpan) End(error) {}

// WithTracer liga o agente a um rastreador.
//
// Nil é ignorado de propósito, como nas outras opções: um adaptador que falhou
// ao subir devolve nil, e nesse caso o certo é o agente seguir mudo em vez de
// não rodar. Telemetria indisponível não pode impedir o trabalho.
func WithTracer(tracer Tracer) Option {
	return func(a *Agent) {
		if tracer != nil {
			a.tracer = tracer
		}
	}
}
