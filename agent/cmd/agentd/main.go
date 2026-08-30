// Comando agentd: roda uma tarefa numa tela do agent computer.
//
// É o ponto de composição — o único lugar do programa que conhece
// implementações concretas. Todo o resto fala com interfaces.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/connectors"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/lock"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/screen"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/skills"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/store"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/tools"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/xai"
	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// agentInstructions define a conduta do agente. Cada regra corresponde a uma
// cláusula da documentação do Grok Bot. A de não contornar barreira é a mais
// importante: um agente que tenta resolver CAPTCHA sozinho viola a documentação
// e costuma derrubar a sessão do site junto.
const agentInstructions = `Você é um agente que opera um computador em nuvem com tela própria.

Regras invioláveis:
1. NUNCA tente contornar senha, verificação em duas etapas, CAPTCHA, confirmação
   de pagamento ou verificação de identidade. Ao encontrar qualquer uma delas,
   chame request_takeover e PARE. Uma pessoa vai resolver e devolver o controle.
2. NUNCA peça senha nem código de uso único em texto na conversa.
3. Guarde trabalho durável em /workspace. O diretório /scratch é apagado a cada
   reconstrução do computador, junto com pacotes instalados manualmente.
4. Este computador é compartilhado por todos os agentes da conta: arquivos e
   credenciais de linha de comando são visíveis a todos. Não grave segredo que
   outro agente não deva usar.
5. Quando a tarefa estiver concluída, responda sem chamar ferramenta nenhuma.

Trabalhe em passos pequenos e confira o resultado de cada um antes de seguir.`

// exitFailure é o código de saída quando a execução falha.
const exitFailure = 1

// main lê os parâmetros e delega, deixando um único ponto de saída para o erro.
func main() {
	var (
		screenNumber = flag.Int("screen", 1, "número da tela do agente (1..9)")
		prompt       = flag.String("prompt", "", "a tarefa a executar")
		taskID       = flag.String("task", "", "id da tarefa (gerado se vazio)")
		resume       = flag.Bool("resume", false, "retoma uma tarefa bloqueada após o take-over")
		abandon      = flag.Bool("abandon", false, "desiste de uma tarefa bloqueada e libera a tela")
		note         = flag.String("note", "", "recado à retomada, dizendo o que foi feito")
		stateDir     = flag.String("state", "/workspace/agent", "diretório de estado durável")
		modelName    = flag.String("model", "", "modelo da xAI (padrão: grok-4.6)")
	)
	flag.Parse()

	if err := run(*screenNumber, *prompt, *taskID, *note, *stateDir, *modelName, *resume, *abandon); err != nil {
		fmt.Fprintf(os.Stderr, "erro: %v\n", err)
		os.Exit(exitFailure)
	}
}

// run monta as dependências concretas e executa a ação pedida.
func run(screenNumber int, prompt, taskID, note, stateDir, modelName string, resume, abandon bool) error {
	// Abandonar é operação local: não chama o modelo nem carrega conectores.
	// Tratar antes do resto evita exigir a chave da API só para liberar uma tela
	// — que é justamente o que se quer fazer quando algo deu errado.
	if abandon {
		taskStore, err := store.NewFileStore(stateDir)
		if err != nil {
			return err
		}
		screenDriver, err := screen.NewXScreen(stateDir + "/status")
		if err != nil {
			return err
		}
		return abandonTask(context.Background(), taskStore, screenDriver, taskID)
	}

	// A chave vem só do ambiente, e quem a coloca lá é o wrapper que a lê do
	// cofre. Assim ela nunca aparece em linha de comando, onde `ps` a exporia a
	// qualquer processo da máquina.
	apiKey := os.Getenv("XAI_API_KEY")
	if apiKey == "" {
		return errors.New("XAI_API_KEY não está no ambiente")
	}

	options := []xai.Option{}
	if modelName != "" {
		options = append(options, xai.WithModel(modelName))
	}
	languageModel, err := xai.NewClient(apiKey, options...)
	if err != nil {
		return err
	}

	taskStore, err := store.NewFileStore(stateDir)
	if err != nil {
		return err
	}
	screenDriver, err := screen.NewXScreen(stateDir + "/status")
	if err != nil {
		return err
	}
	screenLock, err := lock.NewFileLock(stateDir + "/locks")
	if err != nil {
		return err
	}

	// Ferramentas sempre disponíveis, independentes de conector.
	toolset := []ports.Tool{
		tools.NewShell("/workspace"),
		tools.NewTakeover(),
	}

	// Conectores anexados com "@" no texto da tarefa. Só os pedidos entram:
	// a descrição de cada ferramenta vai no prompt a cada iteração, então
	// oferecer o catálogo inteiro custaria token em toda chamada e daria ao
	// modelo acesso a serviços que a tarefa não pediu.
	registry, err := connectors.NewRegistry(stateDir + "/connectors")
	if err != nil {
		return err
	}
	request := domain.ParseTaskRequest(prompt)
	if len(request.Connectors) > 0 {
		attached, missing := registry.ToolsFor(request.Connectors)
		toolset = append(toolset, attached...)
		if len(missing) > 0 {
			// Conector inexistente é aviso, não erro fatal: a pessoa pode ter
			// digitado errado, e derrubar a tarefa por um nome trocado é pior
			// do que seguir sem ele e dizer o que faltou.
			fmt.Fprintf(os.Stderr, "aviso: conector(es) não instalado(s): %s\n", strings.Join(missing, ", "))
		}
		fmt.Printf("conectores anexados: %s (%d ferramentas)\n",
			strings.Join(request.Connectors, ", "), len(attached))
	}
	// Habilidades salvas referenciadas com "/" entram no texto da tarefa. É o
	// que evita reescrever o mesmo procedimento longo a cada vez.
	skillStore, err := skills.NewStore(stateDir + "/skills")
	if err != nil {
		return err
	}
	expanded, missingSkills := skillStore.Expand(request.Skills)
	if len(missingSkills) > 0 {
		fmt.Fprintf(os.Stderr, "aviso: habilidade(s) não encontrada(s): %s\n", strings.Join(missingSkills, ", "))
	}
	if expanded != "" {
		fmt.Printf("habilidades aplicadas: %s\n", strings.Join(request.Skills, ", "))
	}

	// O texto segue sem os marcadores: eles são instrução para o agente, não
	// para o modelo. As habilidades entram DEPOIS do pedido, para o objetivo
	// vir primeiro — instrução longa antes da tarefa faz o modelo tratar o
	// procedimento como o objetivo.
	prompt = request.Prompt + expanded

	agent := service.NewAgent(languageModel, toolset, screenDriver, taskStore, screenLock, time.Now, agentInstructions)

	// Ctrl+C precisa liberar a trava da tela: sem isto, uma interrupção deixaria
	// a tela travada até alguém apagar o arquivo à mão.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if resume {
		return resumeTask(ctx, agent, taskStore, taskID, note)
	}
	return startTask(ctx, agent, taskStore, screenNumber, prompt, taskID)
}

// startTask cria e executa uma tarefa nova, recusando começar se a tela já
// estiver ocupada — é a trava de uma tarefa por tela que a documentação define.
func startTask(ctx context.Context, agent *service.Agent, taskStore *store.FileStore, screenNumber int, prompt, taskID string) error {
	if strings.TrimSpace(prompt) == "" {
		return errors.New("informe a tarefa com -prompt")
	}
	running, err := taskStore.ActiveTaskOnScreen(ctx, screenNumber)
	if err != nil {
		return err
	}
	if running != nil {
		// A mensagem diz O QUE FAZER, e não só o que está errado: quem topa
		// com uma tarefa bloqueada de dias atrás não tem como adivinhar que
		// existe um comando para liberar a tela.
		return fmt.Errorf("%w: tarefa %s está em %s na tela %d.\n"+
			"   Para retomar:  agentd -resume -task %s\n"+
			"   Para desistir: agentd -abandon -task %s",
			domain.ErrScreenBusy, running.ID, running.State, screenNumber,
			running.ID, running.ID)
	}

	id := taskID
	if id == "" {
		id = fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	task, err := domain.NewTask(id, screenNumber, prompt, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("tarefa %s na tela %d\n", task.ID, task.Screen)

	runErr := agent.Run(ctx, task)
	fmt.Printf("estado final: %s\n", task.StatusLine())

	// Bloqueio não é falha: é o comportamento correto diante de uma barreira
	// sensível. Sair com erro aqui faria um script de automação tratar o pedido
	// de ajuda como defeito.
	if task.State == domain.StateBlocked {
		fmt.Printf("\nA tarefa espera você. Abra a tela, resolva o passo e rode:\n")
		fmt.Printf("  agentd -resume -task %s\n", task.ID)
		return nil
	}
	return runErr
}

// abandonTask desiste de uma tarefa bloqueada e libera a tela.
//
// Sem isto, uma tarefa que ficou esperando uma pessoa trava a tela para sempre —
// e como o estado é durável, ela sobrevive a reboot e a rebuild do computador. A
// única saída seria apagar o arquivo à mão, o que ninguém descobre sozinho.
//
// Encontrado no teste integrado: uma tarefa bloqueada no dia anterior impediu
// todas as tarefas seguintes na tela 1, e o sintoma ("a tela já tem uma tarefa
// ativa") não sugeria o que fazer.
func abandonTask(ctx context.Context, taskStore *store.FileStore, screenDriver *screen.XScreen, taskID string) error {
	if taskID == "" {
		return errors.New("informe a tarefa com -task")
	}
	task, err := taskStore.LoadTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("tarefa %s não encontrada", taskID)
	}
	if !task.Active() {
		return fmt.Errorf("tarefa %s já está encerrada (estado %s)", taskID, task.State)
	}
	// Tarefa pendente nunca chegou a rodar, então não há transição de falha
	// possível — ela é simplesmente descartada do disco.
	if task.State == domain.StatePending {
		fmt.Printf("tarefa %s estava pendente e foi descartada\n", taskID)
	} else if err := task.Fail("abandonada por decisão humana", time.Now()); err != nil {
		return err
	}
	if err := taskStore.SaveTask(ctx, task); err != nil {
		return err
	}
	_ = screenDriver.ClearTakeover(ctx, task.Screen)
	_ = screenDriver.ShowStatus(ctx, task.Screen, task.StatusLine())
	fmt.Printf("tela %d liberada\n", task.Screen)
	return nil
}

// resumeTask devolve o controle ao agente depois que a pessoa resolveu o passo.
func resumeTask(ctx context.Context, agent *service.Agent, taskStore *store.FileStore, taskID, note string) error {
	if taskID == "" {
		return errors.New("informe a tarefa com -task")
	}
	task, err := taskStore.LoadTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("tarefa %s não encontrada", taskID)
	}
	if task.State != domain.StateBlocked {
		return fmt.Errorf("tarefa %s não está bloqueada (estado %s)", taskID, task.State)
	}
	resumeErr := agent.Resume(ctx, task, note)
	fmt.Printf("estado final: %s\n", task.StatusLine())
	if task.State == domain.StateBlocked {
		fmt.Printf("\nBloqueou de novo: %s\n", task.BlockDetail)
		return nil
	}
	return resumeErr
}
