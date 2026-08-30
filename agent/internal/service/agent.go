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
	model   ports.LanguageModel
	tools   map[string]ports.Tool
	screen  ports.ScreenDriver
	store   ports.TaskStore
	lock    ports.ScreenLock
	clock   Clock
	sysPrompt string
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
) *Agent {
	byName := make(map[string]ports.Tool, len(tools))
	for _, t := range tools {
		byName[t.Spec().Name] = t
	}
	return &Agent{
		model: model, tools: byName, screen: screen,
		store: store, lock: lock, clock: clock, sysPrompt: systemPrompt,
	}
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
		conv = domain.NewConversation(task.ID, a.sysPrompt)
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

	for i := 0; i < maxIterations; i++ {
		conv.Trim(maxHistoryMessages)
		if err := a.store.SaveConversation(ctx, conv); err != nil {
			return fmt.Errorf("gravando conversa: %w", err)
		}

		completion, err := a.complete(ctx, conv, specs)
		if err != nil {
			_ = task.Fail(fmt.Sprintf("modelo falhou: %v", err), a.clock())
			_ = a.persist(ctx, task)
			return err
		}

		conv.AddAssistant(completion.Content, completion.ToolCalls)

		// Sem chamada de ferramenta, o modelo considerou a tarefa terminada.
		if len(completion.ToolCalls) == 0 {
			if err := task.Finish(a.clock()); err != nil {
				return err
			}
			return a.settle(ctx, task, conv)
		}

		for _, call := range completion.ToolCalls {
			blocked, err := a.runTool(ctx, task, conv, call)
			if err != nil {
				return err
			}
			// Bloqueou: o laço para aqui e devolve o controle. Continuar
			// chamando o modelo enquanto a pessoa não agiu é exatamente o
			// "tentar contornar a verificação" que a documentação proíbe.
			if blocked {
				return a.settle(ctx, task, conv)
			}
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
	return a.persist(ctx, task)
}

// runTool executa uma chamada e devolve se ela bloqueou a tarefa.
//
// Falha de ferramenta NÃO derruba a tarefa: o erro vira conteúdo no histórico e
// o modelo costuma se recuperar sozinho na iteração seguinte. Abortar a cada
// comando com saída diferente de zero tornaria o agente inútil, porque
// `grep` sem resultado já devolve 1.
func (a *Agent) runTool(ctx context.Context, task *domain.Task, conv *domain.Conversation, call domain.ToolCall) (bool, error) {
	tool, ok := a.tools[call.Name]
	if !ok {
		// Modelo inventa nome de ferramenta. Dizer isso a ele no histórico é
		// mais útil do que abortar.
		return false, conv.AddToolResult(call.ID, fmt.Sprintf("ferramenta desconhecida: %q", call.Name))
	}

	_ = a.screen.ShowStatus(ctx, task.Screen, fmt.Sprintf("tela %d: %s", task.Screen, call.Name))

	result, err := tool.Execute(ctx, task.Screen, call.Arguments)
	if err != nil {
		return false, conv.AddToolResult(call.ID, fmt.Sprintf("erro executando %s: %v", call.Name, err))
	}

	if result.BlockRequest != nil {
		req := result.BlockRequest
		if err := task.Block(req.Reason, req.Detail, a.clock()); err != nil {
			// Motivo inválido não pode virar bloqueio silencioso: o agente
			// pararia sem a tela saber o que pedir. Volta como erro ao modelo.
			return false, conv.AddToolResult(call.ID, fmt.Sprintf("pedido de ajuda recusado: %v", err))
		}
		if err := conv.AddToolResult(call.ID, result.Output); err != nil {
			return true, err
		}
		_ = a.screen.RequestTakeover(ctx, task.Screen, req.Reason, req.Detail)
		_ = a.screen.ShowStatus(ctx, task.Screen, task.StatusLine())
		return true, nil
	}

	return false, conv.AddToolResult(call.ID, result.Output)
}

// Resume devolve o controle ao agente depois que a pessoa resolveu o passo
// sensível, e continua a tarefa de onde ela parou.
func (a *Agent) Resume(ctx context.Context, task *domain.Task, humanNote string) error {
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
	// A trava é tomada AQUI, e não dentro do laço, porque o laço é o mesmo do
	// Run — que já a tem. Retomar exige tomá-la de novo: o processo anterior
	// morreu, e outra tarefa pode ter chegado à tela nesse intervalo.
	release, err := a.lock.Acquire(ctx, task.Screen, task.ID)
	if err != nil {
		return fmt.Errorf("tomando a tela %d: %w", task.Screen, err)
	}
	defer func() { _ = release() }()

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
