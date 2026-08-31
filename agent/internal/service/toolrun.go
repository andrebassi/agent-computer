package service

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// toolOutcome é o resultado de UMA chamada, guardado na posição em que o modelo
// a pediu.
type toolOutcome struct {
	call   domain.ToolCall
	result *ports.ToolResult
	err    error
	// known distingue "a ferramenta falhou" de "a ferramenta não existe": o
	// modelo inventa nome com alguma frequência, e as duas coisas pedem
	// mensagens diferentes de volta para ele.
	known bool
}

// runToolCalls executa as chamadas de um turno e devolve os resultados NA ORDEM
// em que o modelo os pediu — nunca na ordem de término.
//
// A ordem é do modelo porque o histórico é o que ele relê na iteração seguinte:
// ordem que muda a cada execução torna a conversa irreprodutível, e nenhum
// defeito daí se reproduz duas vezes igual.
//
// O paralelismo é TUDO OU NADA por turno: só quando todas as chamadas se
// declaram concorrentes. Particionar em blocos criaria um escalonador cujas
// garantias precisariam ser testadas num espaço combinatório — e o caso real que
// paga a conta, várias chamadas de conector no mesmo turno, já é atendido assim.
//
// Efeito colateral que importa: `request_takeover` não é concorrente, então todo
// turno que pede ajuda humana roda exatamente como antes.
func (a *Agent) runToolCalls(ctx context.Context, task *domain.Task, calls []domain.ToolCall) []toolOutcome {
	outcomes := make([]toolOutcome, len(calls))

	if !a.allConcurrent(calls) {
		for i, call := range calls {
			outcomes[i] = a.executeTool(ctx, task, call)
		}
		return outcomes
	}

	// Uma linha de status ANTES do fan-out, e nenhuma goroutine toca a tela: o
	// driver escreve num arquivo por tela, e N escritas simultâneas fariam
	// vencer a ferramenta que terminou primeiro, não a que interessa.
	_ = a.screen.ShowStatus(ctx, task.Screen,
		fmt.Sprintf("tela %d: %d ferramentas em paralelo", task.Screen, len(calls)))

	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(index int, c domain.ToolCall) {
			defer wg.Done()
			// Cada goroutine escreve SÓ na própria posição. Conversation e Task
			// não são seguros para acesso concorrente, e por isso nenhuma delas
			// é tocada aqui — quem as muta é o consumidor sequencial.
			outcomes[index] = a.executeTool(ctx, task, c)
		}(i, call)
	}
	// Espera TODAS, mesmo que uma já tenha pedido take-over. Cancelar as irmãs
	// produziria efeito no mundo — um POST que já saiu — sem registro no
	// histórico, e o histórico é o que alguém lê para saber o que a máquina fez.
	wg.Wait()
	return outcomes
}

// allConcurrent diz se TODAS as chamadas do turno podem correr juntas.
//
// Ferramenta desconhecida conta como não concorrente: se o modelo inventou o
// nome, o turno inteiro roda em série e a mensagem de erro sai pelo caminho
// normal, sem um ramo especial dentro do fan-out.
func (a *Agent) allConcurrent(calls []domain.ToolCall) bool {
	if len(calls) < 2 {
		return false
	}
	for _, call := range calls {
		tool, ok := a.tools[call.Name]
		if !ok || !tool.Spec().Concurrent {
			return false
		}
	}
	return true
}

// executeTool roda UMA ferramenta, sem tocar em conversa, tarefa ou tela.
//
// Essa abstinência é o que a torna segura para rodar em goroutine: tudo que ela
// devolve vai para uma posição própria do vetor, e a mutação de estado acontece
// depois, em ordem, no consumidor.
// A instrumentação daqui é o que responde "onde foi o tempo".
//
// Até agora o repositório inteiro media duração em DOIS lugares — o turno do
// modelo e o relógio do guardrail —, e nenhuma ferramenta era cronometrada. Com
// isso, "a tarefa demorou 4 minutos" não se decompunha: não dava para saber se
// foi o modelo pensando, o navegador carregando ou um comando pendurado.
func (a *Agent) executeTool(ctx context.Context, task *domain.Task, call domain.ToolCall) toolOutcome {
	ctx, span := a.tracer.Start(ctx, spanExecuteTool+" "+call.Name,
		String(attrGenAIOperation, "execute_tool"),
		String(attrGenAIToolName, call.Name),
		String(attrGenAIToolCallID, call.ID),
		// O HASH dos argumentos, nunca os argumentos.
		//
		// Reaproveita a chave do detector de laço, então o número que aparece
		// aqui é o MESMO que ele compara — dá para ver no trace que três
		// chamadas eram idênticas sem que o comando escrito pelo modelo saia da
		// máquina. Um argumento de shell pode conter caminho, credencial
		// colada por engano, ou o conteúdo de um arquivo.
		String(attrToolArgsHash, failureKey(call.Name, call.Arguments)),
	)

	tool, ok := a.tools[call.Name]
	if !ok {
		// Nome inventado pelo modelo. É sinal, não erro do sistema: o trecho
		// fecha sem erro, com a marca, para não poluir a taxa de falha com o que
		// é comportamento conhecido do modelo.
		span.SetAttributes(Bool(attrToolUnknown, true))
		span.End(nil)
		return toolOutcome{call: call}
	}
	// Status por chamada só no caminho SERIAL: no paralelo, a linha única já foi
	// escrita antes do fan-out.
	if !tool.Spec().Concurrent {
		_ = a.screen.ShowStatus(ctx, task.Screen, fmt.Sprintf("tela %d: %s", task.Screen, call.Name))
	}
	startedAt := a.clock()
	result, err := tool.Execute(ctx, task.Screen, call.Arguments)
	// A duração por ferramenta é o número que faltava: até agora "a tarefa
	// demorou" não se decompunha em modelo, navegador e shell. Histograma e não
	// média — uma chamada de 40 s entre noventa de 2 s desaparece numa média, e
	// é ela que alguém está tentando explicar.
	//
	// O nome da ferramenta é rótulo seguro: são nove fixas mais as dos
	// conectores instalados, que o operador controla. Os ARGUMENTOS ficam de
	// fora, aqui como no trecho.
	failed := result != nil && result.Failed
	a.meter.RecordDuration(ctx, metricToolDuration, a.clock().Sub(startedAt).Seconds(),
		String(attrToolName, call.Name), Bool(attrToolFailed, failed))

	// `Failed` vira ATRIBUTO, não status de erro do trecho.
	//
	// Ferramenta que falha não derruba a tarefa — `grep` sem resultado devolve
	// 1, e isso é rotina. Marcar o trecho como erro poria toda tarefa saudável
	// na taxa de erro, e a primeira reação de quem olhasse o painel seria
	// desligar o alerta. Só o `err` do porto, que é falha de verdade, marca.
	if result != nil {
		span.SetAttributes(Bool(attrToolFailed, result.Failed))
	}
	span.End(err)
	return toolOutcome{call: call, result: result, err: err, known: true}
}

// applyOutcome anexa o resultado ao histórico e trata o pedido de take-over.
//
// Devolve se a tarefa passou a estar bloqueada. Roda SEMPRE em série, um
// resultado por vez, na ordem do modelo — é o único ponto onde a conversa e a
// tarefa são mutadas.
func (a *Agent) applyOutcome(ctx context.Context, task *domain.Task, conv *domain.Conversation, out toolOutcome) (bool, error) {
	if !out.known {
		// Modelo inventou nome de ferramenta. Dizer isso a ele no histórico é
		// mais útil do que abortar.
		return false, conv.AddToolResult(out.call.ID, fmt.Sprintf("ferramenta desconhecida: %q", out.call.Name))
	}
	if out.err != nil {
		// Falha de ferramenta NÃO derruba a tarefa: o erro vira conteúdo no
		// histórico e o modelo costuma se recuperar sozinho. Abortar a cada
		// comando com saída diferente de zero tornaria o agente inútil, porque
		// `grep` sem resultado já devolve 1.
		return false, conv.AddToolResult(out.call.ID,
			fmt.Sprintf("erro executando %s: %v", out.call.Name, out.err))
	}

	if out.result.BlockRequest == nil {
		return false, conv.AddToolResult(out.call.ID, out.result.Output)
	}

	req := out.result.BlockRequest
	// Duas ferramentas do mesmo turno podem pedir take-over. Vence a PRIMEIRA na
	// ordem do modelo, deterministicamente — e a segunda recebe uma explicação,
	// em vez do "pedido recusado" que a transição inválida produziria e que
	// sugeriria má-formação onde só houve concorrência.
	if task.State == domain.StateBlocked {
		return true, conv.AddToolResult(out.call.ID, fmt.Sprintf(
			"a tarefa já está bloqueada por outro pedido nesta mesma rodada (%s); o seu não foi aplicado",
			task.BlockReason))
	}

	if err := task.Block(req.Reason, req.Detail, a.clock()); err != nil {
		// Motivo inválido não pode virar bloqueio silencioso: o agente pararia
		// sem a tela saber o que pedir. Volta como erro ao modelo.
		return false, conv.AddToolResult(out.call.ID, fmt.Sprintf("pedido de ajuda recusado: %v", err))
	}
	if err := conv.AddToolResult(out.call.ID, out.result.Output); err != nil {
		return true, err
	}
	// O pedido de ajuda é o evento mais importante que esta máquina produz: é o
	// único que exige uma PESSOA. Marcá-lo no trecho é o que permite responder
	// "quanto tempo esta tarefa passou esperando alguém" sem abrir a conversa.
	//
	// Só o motivo, que é enum fechado de seis. O `req.Detail` fica de fora: ele
	// descreve a barreira que o modelo encontrou e pode citar o conteúdo da
	// página — inclusive de uma tela de login.
	a.tracer.AddEvent(ctx, eventTakeoverRequested,
		String(attrBlockReason, string(req.Reason)),
	)

	_ = a.screen.RequestTakeover(ctx, task.Screen, req.Reason, req.Detail)
	_ = a.screen.ShowStatus(ctx, task.Screen, task.StatusLine())
	return true, nil
}
