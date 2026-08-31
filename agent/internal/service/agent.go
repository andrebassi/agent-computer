// Package service orquestra o domínio e os portos. É a camada que traduz "o
// modelo pediu para rodar um comando" em transições de estado e efeitos.
//
// Fica fora de domain porque precisa dos portos, e domain não importa nada.
package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// maxIterations limita quantas vezes o laço chama o modelo numa tarefa.
//
// Sem teto, um agente que erra a mesma chamada de ferramenta em ciclo queima
// dinheiro em tokens até alguém perceber. O número é generoso para tarefa real
// de navegador, onde cada clique é uma iteração.
const maxIterations = 60

// maxHistoryMessages limita o histórico enviado ao modelo. Conversa longa custa
// caro em token de entrada, que é cobrado a cada iteração.
const maxHistoryMessages = 80

// ErrMaxIterations sinaliza que a tarefa bateu o teto de iterações.
var ErrMaxIterations = errors.New("limite de iterações atingido")

// Clock permite congelar o tempo no teste. Sem isto, cada asserção sobre
// carimbo de tempo viraria um teste intermitente.
type Clock func() time.Time

// Agent roda tarefas numa tela.
type Agent struct {
	model     ports.LanguageModel
	tools     map[string]ports.Tool
	screen    ports.ScreenDriver
	store     ports.TaskStore
	lock      ports.ScreenLock
	clock     Clock
	sysPrompt string
	// sink publica os fatos da tarefa para fora. Nunca é nil — o padrão descarta
	// tudo, o que evita uma checagem em cada ponto de publicação.
	sink ports.EventSink
	// journal escreve os quatro arquivos de memória. Nunca é nil, pelo mesmo
	// motivo do sink.
	journal GuardrailJournal
	// taskBudget é o tempo concedido à tarefa, usado pelo detector de tempo de
	// parede. Zero desliga o detector — é o caso do CLI, que não tem teto.
	taskBudget time.Duration
}

// discardSink é o destino padrão: descarta tudo.
//
// Mora AQUI, e não no adaptador de eventos, por causa da direção das setas: o
// serviço depende de portos, nunca de adaptadores. Importar o pacote de
// adaptadores só para pegar um objeto vazio inverteria a dependência que a
// arquitetura inteira existe para manter.
type discardSink struct{}

// Publish descarta o fato sem fazer nada.
func (discardSink) Publish(context.Context, domain.TaskEvent) error { return nil }

// discardJournal é o diário padrão: não grava nada.
//
// Mesmo motivo do discardSink — sem ele, cada um dos pontos de escrita
// precisaria de uma checagem de nil, e a que faltasse viraria pânico só no
// caminho de falha, que é o menos exercitado.
type discardJournal struct{}

func (discardJournal) RecordActivity(context.Context, string) error { return nil }
func (discardJournal) RecordError(context.Context, string) error    { return nil }
func (discardJournal) RecordProgress(context.Context, string) error { return nil }
func (discardJournal) LearnLesson(context.Context, GuardrailKind, string) error {
	return nil
}

// Lessons devolve vazio: sem diário configurado não há o que lembrar.
func (discardJournal) Lessons() (string, error) { return "", nil }

// Option configura o agente sem mudar a assinatura de NewAgent.
//
// Existe para acrescentar dependências opcionais sem quebrar as chamadas atuais:
// um oitavo parâmetro posicional obrigaria a editar o ponto de composição e
// todos os testes, e cada edição dessas é uma chance de trocar a ordem de dois
// argumentos do mesmo tipo sem o compilador notar.
type Option func(*Agent)

// WithEventSink liga o agente a um destino de eventos.
func WithEventSink(sink ports.EventSink) Option {
	return func(a *Agent) {
		if sink != nil {
			a.sink = sink
		}
	}
}

// WithGuardrailJournal liga o agente aos quatro arquivos de memória.
func WithGuardrailJournal(journal GuardrailJournal) Option {
	return func(a *Agent) {
		if journal != nil {
			a.journal = journal
		}
	}
}

// WithTaskBudget informa quanto tempo a tarefa tem.
//
// Quem sabe disso é o supervisor, que monta o contexto com prazo; o laço não
// tem como descobrir sozinho — `context.Deadline` daria o instante final, mas
// não o total concedido, e o detector precisa da FRAÇÃO consumida.
func WithTaskBudget(budget time.Duration) Option {
	return func(a *Agent) {
		if budget > 0 {
			a.taskBudget = budget
		}
	}
}

// NewAgent monta o agente com suas dependências. Todas são interfaces: o teste
// injeta duplos e o laço roda sem rede, sem disco e sem servidor X.
func NewAgent(
	model ports.LanguageModel,
	tools []ports.Tool,
	screen ports.ScreenDriver,
	store ports.TaskStore,
	lock ports.ScreenLock,
	clock Clock,
	systemPrompt string,
	opts ...Option,
) *Agent {
	byName := make(map[string]ports.Tool, len(tools))
	for _, t := range tools {
		byName[t.Spec().Name] = t
	}
	agent := &Agent{
		model: model, tools: byName, screen: screen,
		store: store, lock: lock, clock: clock, sysPrompt: systemPrompt,
		// Descartar por padrão, e não nil: sem destino configurado o agente
		// funciona igual, e não há um caminho onde esquecer a checagem de nil
		// vire pânico em produção.
		sink:    discardSink{},
		journal: discardJournal{},
	}
	for _, opt := range opts {
		opt(agent)
	}
	return agent
}

// Run executa uma tarefa do início ao fim, ou até ela bloquear pedindo uma
// pessoa.
//
// A trava de tela é adquirida antes de qualquer outra coisa: a documentação diz
// que um agente roda uma tarefa por tela de cada vez, e duas tarefas disputando
// o mesmo teclado produzem cliques intercalados que não falham — só fazem a
// coisa errada.
func (a *Agent) Run(ctx context.Context, task *domain.Task) error {
	release, err := a.lock.Acquire(ctx, task.Screen, task.ID)
	if err != nil {
		return fmt.Errorf("tomando a tela %d: %w", task.Screen, err)
	}
	defer func() { _ = release() }()

	if err := task.Start(a.clock()); err != nil {
		return err
	}
	if err := a.persist(ctx, task); err != nil {
		return err
	}

	conv, err := a.store.LoadConversation(ctx, task.ID)
	if err != nil || conv == nil {
		// As lições entram no prompt de sistema da conversa NOVA. Numa conversa
		// carregada elas já estão lá, e reinjetá-las duplicaria o bloco.
		conv = domain.NewConversation(task.ID, a.systemPromptWithLessons())
		conv.AddUser(task.Prompt)
	}

	return a.iterate(ctx, task, conv)
}

// iterate é O laço do agente — um só, usado tanto pelo início quanto pela
// retomada.
//
// Existir uma única vez é o ponto. Ele já foi escrito duas vezes (Run e
// continueLoop), as cópias divergiram, e as duas divergências eram defeitos: a
// retomada não gravava a resposta final, e nenhuma das duas gravava o turno que
// pedia take-over. Toda melhoria futura no laço — retry, paralelismo, evento —
// entra aqui uma vez.
//
// A trava da tela NÃO é adquirida aqui: quem chama já a tem, e flock é por
// descritor aberto — uma segunda aquisição no mesmo processo colidiria consigo
// mesma.
func (a *Agent) iterate(ctx context.Context, task *domain.Task, conv *domain.Conversation) error {
	specs := a.toolSpecs()
	guard := newGuardrailState(a.clock(), a.taskBudget)

	for i := 0; i < maxIterations; i++ {
		// Detectores ANTES de chamar o modelo: parar depois de gastar o turno
		// seria pagar exatamente aquilo que o teto existe para evitar.
		//
		// A ordem entre eles é deliberada — turnos primeiro, porque é o teto
		// duro; tempo depois, porque é o que mais depende de quando se olha.
		if hit := checkTurnCap(task); hit != nil {
			if err := a.applyHit(ctx, task, conv, hit); err != nil {
				return err
			}
			return a.settle(ctx, task, conv)
		}
		if hit := guard.checkWallClock(a.clock()); hit != nil {
			if err := a.applyHit(ctx, task, conv, hit); err != nil {
				return err
			}
			return a.settle(ctx, task, conv)
		}

		conv.Trim(maxHistoryMessages)
		if err := a.store.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("gravando conversa: %w", err)
		}

		turnStarted := a.clock()
		// O contador é incrementado ANTES da chamada, e não depois: um turno que
		// falha no meio consumiu recurso do mesmo jeito, e não contá-lo deixaria
		// o teto ser furado justamente pelo caminho que mais repete.
		task.TurnsUsed++

		completion, err := a.complete(ctx, conv, specs)
		if err != nil {
			_ = task.Fail(fmt.Sprintf("modelo falhou: %v", err), a.clock())
			// settle, e não persist: sem isto a tarefa morria em SILÊNCIO —
			// gravava o estado e ninguém era avisado. É o caminho de falha, que
			// é justamente quando alguém precisa saber.
			_ = a.settle(ctx, task, conv)
			return err
		}

		conv.AddAssistant(completion.Content, completion.ToolCalls)

		a.recordTurn(ctx, task, i, completion, a.clock().Sub(turnStarted))

		// Sem chamada de ferramenta, o modelo considerou a tarefa terminada —
		// MENOS quando a resposta foi cortada no meio.
		//
		// `StopReason` era preenchido pelo adaptador e nunca lido: uma resposta
		// truncada por limite de saída (`finish_reason: "length"`) chega sem
		// chamada de ferramenta e era tratada como conclusão bem-sucedida. A
		// tarefa terminava `done`, com a resposta pela metade e ninguém sabendo.
		if len(completion.ToolCalls) == 0 {
			if truncatedStop(completion.StopReason) {
				hit := &GuardrailHit{
					Kind: GuardrailTruncated,
					Detail: fmt.Sprintf(
						"a resposta do modelo foi cortada por limite de saída (%q) e a tarefa parou. "+
							"Isso não é conclusão: o que veio pode estar pela metade.",
						completion.StopReason),
					Lesson: "",
				}
				if err := a.applyHit(ctx, task, conv, hit); err != nil {
					return err
				}
				return a.settle(ctx, task, conv)
			}
			if err := task.Finish(a.clock()); err != nil {
				return err
			}
			return a.settle(ctx, task, conv)
		}

		// A execução pode ser paralela; a APLICAÇÃO é sempre em série, na ordem
		// em que o modelo pediu.
		outcomes := a.runToolCalls(ctx, task, completion.ToolCalls)
		blockedInTurn := false
		for _, out := range outcomes {
			// O detector de laço vê o resultado ANTES de ele virar histórico.
			// `ToolResult.Failed` era escrito por toda ferramenta e lido por
			// ninguém; é aqui que ele passa a ter consumidor.
			if hit := a.observeOutcome(ctx, guard, out); hit != nil {
				if err := a.applyHit(ctx, task, conv, hit); err != nil {
					return err
				}
				// O resultado ainda entra no histórico: sem ele, quem abrir a
				// conversa vê o bloqueio sem a falha que o causou.
				_, _ = a.applyOutcome(ctx, task, conv, out)
				return a.settle(ctx, task, conv)
			}
			blocked, err := a.applyOutcome(ctx, task, conv, out)
			if err != nil {
				return err
			}
			// Não sai do laço aqui: os resultados das ferramentas irmãs que já
			// rodaram precisam entrar no histórico. Sair agora produziria efeito
			// no mundo sem registro — e o histórico é o que alguém lê depois
			// para saber o que a máquina fez.
			if blocked {
				blockedInTurn = true
			}
		}
		// Bloqueou: o laço para aqui e devolve o controle. Continuar chamando o
		// modelo enquanto a pessoa não agiu é exatamente o "tentar contornar a
		// verificação" que a documentação proíbe.
		if blockedInTurn {
			return a.settle(ctx, task, conv)
		}
	}

	_ = task.Fail(ErrMaxIterations.Error(), a.clock())
	_ = a.settle(ctx, task, conv)
	return ErrMaxIterations
}

// complete chama o modelo e, se a janela de contexto estourar, encurta o
// histórico e tenta DE NOVO — uma vez só.
//
// A divisão de responsabilidade é deliberada: o adaptador reconhece a evidência
// (o código HTTP do fornecedor) e traduz para o sentinela do porto; aqui se
// decide o que fazer, porque o que pode ser descartado de uma conversa é regra
// de produto — a instrução de sistema fica, resultado de ferramenta não pode
// ficar órfão da chamada que o gerou.
//
// Uma tentativa, e não um laço: se comprimir metade do histórico não bastou, o
// problema não é o tamanho — é uma única mensagem gigante, e comprimir de novo
// só descartaria o pedido original. Melhor falhar dizendo isso.
//
// Note que NÃO existe compressão preventiva. Com cache de prompt, encurtar o
// histórico reescreve o prefixo cacheado e faz pagar preço cheio justamente
// pelos tokens que a compressão tentaria economizar. Comprimir só quando o
// modelo recusa é o que mantém o cache útil.
func (a *Agent) complete(ctx context.Context, conv *domain.Conversation, specs []ports.ToolSpec) (*ports.Completion, error) {
	completion, err := a.model.Complete(ctx, conv.Messages, specs)
	if !errors.Is(err, ports.ErrContextTooLong) {
		return completion, err
	}
	if !conv.Compact() {
		return nil, fmt.Errorf("%w e não há histórico para descartar: %v", ports.ErrContextTooLong, err)
	}
	return a.model.Complete(ctx, conv.Messages, specs)
}

// settle encerra um turno gravando o DURÁVEL primeiro: conversa, depois tarefa,
// depois tela.
//
// A conversa é gravada no INÍCIO de cada iteração, então tudo que acontece
// DEPOIS dessa gravação — a resposta final do agente, e o resultado da
// ferramenta que pediu take-over — só chega ao disco se alguém gravar de novo no
// fim. Sem isto:
//
//   - quem lesse o histórico não encontrava a conclusão da tarefa;
//   - e a retomada carregava um histórico onde o agente NUNCA pediu ajuda, então
//     ele voltava sem saber por que tinha parado.
//
// A ordem importa: se o processo morrer no meio, o disco tem a verdade. O
// inverso deixaria a tela anunciando um estado que não foi gravado.
func (a *Agent) settle(ctx context.Context, task *domain.Task, conv *domain.Conversation) error {
	if err := a.store.SaveConversation(ctx, conv); err != nil {
		return fmt.Errorf("gravando conversa: %w", err)
	}
	if err := a.persist(ctx, task); err != nil {
		return err
	}
	a.recordOutcome(ctx, task, conv)
	a.publish(ctx, task, conv)
	return nil
}

// recordOutcome anota o desfecho da tarefa no diário de progresso.
//
// `settle` é o ÚNICO ponto por onde toda tarefa passa ao parar — concluída,
// falhada ou bloqueada. Anotar em qualquer outro lugar deixaria um dos três
// desfechos de fora, e seria justamente o que ninguém percebe: o arquivo
// existiria, teria conteúdo, e estaria incompleto.
//
// Não devolve erro, pelo mesmo contrato do `publish`: registrar é efeito
// colateral, e um disco cheio não pode transformar tarefa concluída em falha.
func (a *Agent) recordOutcome(ctx context.Context, task *domain.Task, conv *domain.Conversation) {
	line := fmt.Sprintf("tarefa=%s tela=%d estado=%s turnos=%d",
		task.ID, task.Screen, task.State, task.TurnsUsed)
	switch task.State {
	case domain.StateBlocked:
		line += fmt.Sprintf(" motivo=%s detalhe=%s", task.BlockReason, task.BlockDetail)
	case domain.StateFailed:
		line += fmt.Sprintf(" falha=%s", task.Failure)
	case domain.StateDone:
		// A resposta entra RESUMIDA: o progresso é para saber o que aconteceu,
		// e o texto inteiro já está na conversa gravada.
		answer := strings.ReplaceAll(conv.LastAnswer(), "\n", " ")
		if len(answer) > 160 {
			answer = answer[:160] + "…"
		}
		if answer != "" {
			line += " resposta=" + answer
		}
	}
	_ = a.journal.RecordProgress(ctx, line)
}

// publish avisa o mundo de fora que a tarefa parou.
//
// Não devolve erro, e isso é o contrato: avisar é EFEITO COLATERAL. Um destino
// fora do ar não pode transformar tarefa concluída em tarefa falhada — o
// trabalho foi feito, e o disco já registrou. Falha aqui vira relato no próprio
// histórico, que é onde alguém procuraria depois.
//
// Vem depois do durável de propósito: se o processo morrer no meio, quem
// consulta o disco vê a verdade. O inverso avisaria "concluída" com o disco
// dizendo outra coisa.
func (a *Agent) publish(ctx context.Context, task *domain.Task, conv *domain.Conversation) {
	event, ok := domain.NewTaskEvent(task, conv.LastAnswer(), a.clock())
	if !ok {
		return
	}
	if err := a.sink.Publish(ctx, event); err != nil {
		// O aviso falhou, mas a tarefa não. Registrar no histórico é melhor que
		// engolir: quem for ler a conversa descobre que houve um aviso perdido,
		// em vez de concluir que nunca houve o que avisar.
		_ = conv.AddSystemNote(fmt.Sprintf("aviso não entregue: %v", err))
		_ = a.store.SaveConversation(ctx, conv)
	}
}

// Resume devolve o controle ao agente depois que a pessoa resolveu o passo
// sensível, e continua a tarefa de onde ela parou.
func (a *Agent) Resume(ctx context.Context, task *domain.Task, humanNote string) error {
	// A TRAVA VEM PRIMEIRO, antes de mexer no estado.
	//
	// A ordem inversa custava a tarefa inteira: `task.Resume` mudava para
	// `running`, `persist` gravava, e só então a trava era tentada. Com a tela
	// ocupada, o erro subia para o supervisor, que via `Active() == true` e
	// chamava `markFailed` — uma tarefa BLOQUEADA, esperando uma pessoa, virava
	// `failed`, e o trabalho e o pedido de ajuda iam junto.
	//
	// Tomando antes, o caminho de erro devolve a tarefa intacta: ela continua
	// `blocked`, e quem chamou tenta de novo quando a tela vagar.
	release, err := a.lock.Acquire(ctx, task.Screen, task.ID)
	if err != nil {
		return fmt.Errorf("tomando a tela %d: %w", task.Screen, err)
	}
	defer func() { _ = release() }()

	if err := task.Resume(a.clock()); err != nil {
		return err
	}
	_ = a.screen.ClearTakeover(ctx, task.Screen)

	conv, err := a.store.LoadConversation(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("carregando conversa: %w", err)
	}
	if conv == nil {
		return fmt.Errorf("conversa da tarefa %s não encontrada", task.ID)
	}
	resumeNote := humanNote
	if resumeNote == "" {
		resumeNote = "o passo bloqueado foi concluído por uma pessoa; continue de onde parou"
	}
	conv.AddUser(resumeNote)
	if err := a.store.SaveConversation(ctx, conv); err != nil {
		return err
	}
	if err := a.persist(ctx, task); err != nil {
		return err
	}

	return a.iterate(ctx, task, conv)
}

// toolSpecs monta a lista de ferramentas oferecidas ao modelo, em ordem estável.
//
// A ordenação não é estética: `a.tools` é um MAPA, e iterar um mapa em Go dá
// ordem diferente a cada chamada. Isso muda o prefixo do prompt entre iterações
// da MESMA tarefa, e prefixo instável invalida o cache do fornecedor — que na
// xAI vale 75% de desconto no token de entrada (US$ 0,50/M contra 2,00).
//
// Ou seja: sem estas duas linhas, cada iteração pagava preço cheio por um
// prompt praticamente idêntico ao anterior. E, de quebra, o comportamento do
// agente deixava de ser reproduzível entre execuções.
func (a *Agent) toolSpecs() []ports.ToolSpec {
	specs := make([]ports.ToolSpec, 0, len(a.tools))
	for _, t := range a.tools {
		specs = append(specs, t.Spec())
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

// persist grava a tarefa e reflete o estado na tela, que é o "current status"
// pedido pela documentação.
func (a *Agent) persist(ctx context.Context, task *domain.Task) error {
	_ = a.screen.ShowStatus(ctx, task.Screen, task.StatusLine())
	return a.store.SaveTask(ctx, task)
}
