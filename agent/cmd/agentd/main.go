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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/connectors"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/events"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/journal"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/lock"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/pricing"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/runners"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/screen"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/skills"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/store"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/tools"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/vault"
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
2. NUNCA peça senha nem código de uso único em text na conversa.
3. Guarde trabalho durável em /workspace. O diretório /scratch é apagado a cada
   reconstrução do computador, junto com pacotes instalados manualmente.
4. Este computador é compartilhado por todos os agentes da conta: arquivos e
   credenciais de linha de comando são visíveis a todos. Não grave segredo que
   outro agente não deva usar.
5. Tarefa de CÓDIGO — escrever, corrigir, refatorar, mexer em vários arquivos —
   entregue a delegate_to_code, que é especializado nisso. Navegar, chamar API
   por conector e rodar comando simples você faz melhor e mais barato sozinho.
6. Quando a tarefa estiver concluída, responda sem chamar ferramenta nenhuma.

Trabalhe em passos pequenos e confira o resultado de cada um before de seguir.`

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
		catalog      = flag.Bool("catalog", false, "gerencia conectores e habilidades; use -catalog list")
		notifyDrain  = flag.Bool("notify-drain", false, "entrega os avisos enfileirados e limpa a fila")
		webhookURL   = flag.String("webhook", "", "destino HTTP dos avisos; sem ele, -notify-drain só lista")
		serveHTTP    = flag.Bool("serve", false, "sobe a porta HTTP em vez de rodar uma tarefa")
		listenAddr   = flag.String("listen", "127.0.0.1:8787", "endereço de escuta; use o IP da malha, NUNCA 0.0.0.0")
		tokenFile    = flag.String("token-file", "", "arquivo do token da API (padrão: <state>/api-token)")
		taskTimeout  = flag.Duration("task-timeout", 2*time.Hour, "teto de tempo de uma tarefa")
		vaultInit    = flag.Bool("vault-init", false, "cria o cofre e grava segredos lidos como chave=valor na entrada padrão")
		vaultCheck   = flag.Bool("vault-check", false, "confere se o cofre ABRE com a identidade desta máquina")
		connProbe    = flag.String("connector-probe", "", "tenta alcançar uma URL pelo mesmo caminho de um conector, e diz o que aconteceu")
	)
	flag.Parse()

	opts := runOptions{
		screen: *screenNumber, prompt: *prompt, taskID: *taskID, note: *note,
		stateDir: *stateDir, model: *modelName, webhook: *webhookURL,
		listen: *listenAddr, tokenFile: *tokenFile, taskTimeout: *taskTimeout,
		resume: *resume, abandon: *abandon, catalog: *catalog, drain: *notifyDrain,
		serve: *serveHTTP, vaultInit: *vaultInit, vaultCheck: *vaultCheck,
		connectorProbe: *connProbe,
		rest:           flag.Args(),
	}
	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "erro: %v\n", err)
		os.Exit(exitFailure)
	}
}

// runOptions agrupa o que veio da linha de comando.
//
// Vira struct porque a lista de parâmetros passou de dez, e uma chamada com dez
// posicionais — quatro deles bool seguidos — troca dois argumentos de lugar sem
// o compilador notar.
type runOptions struct {
	screen                          int
	prompt, taskID, note, stateDir  string
	model, webhook                  string
	listen, tokenFile               string
	taskTimeout                     time.Duration
	resume, abandon, catalog, drain bool
	serve, vaultInit, vaultCheck    bool
	connectorProbe                  string
	rest                            []string
}

// run monta as dependências concretas e executa a ação pedida.
func run(o runOptions) error {
	screenNumber, prompt, taskID := o.screen, o.prompt, o.taskID
	note, stateDir, modelName := o.note, o.stateDir, o.model
	resume, abandon, catalog, rest := o.resume, o.abandon, o.catalog, o.rest

	// Provisionar o cofre vem ANTES de qualquer coisa que leia segredo: é o
	// passo que faz os outros funcionarem, e exigir chave de modelo aqui seria
	// pedir a credencial que ainda não há onde guardar.
	if o.vaultInit {
		return runVaultInit(context.Background(), stateDir, os.Stdin)
	}

	// Conferir o cofre vem ANTES de qualquer coisa que dependa dele, e e uma
	// operacao de LEITURA de verdade.
	//
	// Existe porque nenhum outro comando prova que o cofre abre: `-catalog list`
	// so lista conectores, e `-vault-init` GRAVA sem reclamar mesmo num cofre
	// que nao se le -- ele cifra para os destinatarios que achou no store.
	//
	// O estado que isso detecta acontece toda reconstrucao da maquina: a
	// identidade age mora no disco do SISTEMA (deliberado -- e o que faz a foto
	// do volume ser inutil sozinha), o store fica no volume e sobrevive, e passa
	// a estar cifrado para uma chave destruida. Escrever num cofre ilegivel e o
	// pior desfecho possivel, porque parece ter funcionado.
	if o.vaultCheck {
		return runVaultCheck(context.Background(), stateDir)
	}

	// Sondar destino de conector nao depende de cofre nem de modelo: e uma
	// requisicao GET pelo mesmo cliente que as ferramentas de conector usam.
	//
	// Existe pelo mesmo motivo do `-vault-check`: e a unica forma de provar NA
	// MAQUINA que um destino interno e recusado. No Mac nao ha metadata de nuvem
	// para alcancar, e um teste que passa aqui e la por motivos diferentes nao
	// prova a mesma coisa.
	if o.connectorProbe != "" {
		return runConnectorProbe(context.Background(), o.connectorProbe)
	}

	// Gerenciar catálogo é operação local, como abandonar: nada de modelo nem
	// de chave da API.
	if catalog {
		return runCatalog(stateDir, rest)
	}

	// Drenar avisos é operação local e roda em PROCESSO SEPARADO, chamado por
	// timer. É o que satisfaz o requisito de a entrega não depender da conexão
	// que iniciou a tarefa — e por isso não pede chave de modelo nenhuma.
	if o.drain {
		// O destino também pode vir do ambiente, e a unidade systemd usa esse
		// caminho. Antes ela montava a linha de comando com `sh -c`, e o valor
		// era interpolado entre aspas dentro de um shell — um valor que fechasse
		// a aspa emendaria outro comando. Lendo aqui, não existe shell no meio.
		webhook := o.webhook
		if webhook == "" {
			webhook = strings.TrimSpace(os.Getenv("AGENT_WEBHOOK"))
		}
		return runDrain(context.Background(), stateDir, webhook)
	}

	// A porta HTTP precisa do modelo, porque as tarefas que ela cria o chamam.
	if o.serve {
		d, err := buildDeps(stateDir, modelName, true, false)
		if err != nil {
			return err
		}
		tokenPath := o.tokenFile
		if tokenPath == "" {
			tokenPath = stateDir + "/api-token"
		}
		return serve(context.Background(), d, o.listen, tokenPath, o.taskTimeout)
	}

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

	// A chave vem do cofre cifrado e, na falta dele, do ambiente. Nunca de
	// argumento: `ps` mostra a linha de comando de qualquer processo a qualquer
	// usuário da máquina.
	apiKey, source, err := resolveModelKey(context.Background(), stateDir)
	if err != nil {
		return err
	}
	if source != vault.SourceVault {
		// A origem é dita em voz alta porque uma máquina que caiu para o
		// ambiente parece idêntica a uma que usa o cofre — e a diferença é
		// exatamente a que uma auditoria procura.
		fmt.Fprintf(os.Stderr, "aviso: chave do modelo veio do %s, não do cofre\n", source)
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
		tools.NewShellSandboxed("/workspace", toolSandbox()),
		tools.NewTakeover(),
	}

	// Ferramentas de navegador: é o que permite ao agente PILOTAR o Chrome da
	// própria tela, e não apenas tê-lo por perto. Elas falam com a porta de
	// depuração local, que nunca sai de 127.0.0.1 — a porta dá controle total do
	// navegador, incluindo ler cookie de sessão.
	toolset = append(toolset, tools.NewBrowserTools(stateDir+"/screenshots")...)

	// Delegação a um agente de código. Os dois não se substituem: este navega,
	// chama API e sabe parar numa barreira sensível; o outro edita arquivo, mexe
	// em git e abre subagentes. A ferramenta existe pelo caso misto — "leia o
	// site e ajuste o código conforme" —, que nenhum dos dois faz sozinho.
	// O catálogo é lido aqui também: pelo CLI a delegação existe igual, e um
	// runner cadastrado que só funcionasse pela porta HTTP seria surpresa.
	runnerCatalog, catalogErr := runners.Load(filepath.Join(stateDir, "runners.json"))
	if catalogErr != nil {
		fmt.Fprintf(os.Stderr, "aviso: catálogo de runners ignorado: %v\n", catalogErr)
		runnerCatalog, _ = runners.Parse([]byte("{}"))
	}
	toolset = append(toolset, tools.NewDelegateSandboxed("/workspace", stateDir+"/anthropic.env", toolSandbox(), tools.WithRunners(runnerCatalog)))

	// Conectores anexados com "@" no texto da tarefa. Só os pedidos entram:
	// a descrição de cada ferramenta vai no prompt a cada iteração, então
	// oferecer o catálogo inteiro custaria token em toda chamada e daria ao
	// modelo acesso a serviços que a tarefa não pediu.
	registry, err := connectors.NewRegistry(stateDir + "/connectors")
	if err != nil {
		return err
	}
	// Mesma regra do caminho do serviço: conector lê credencial do cofre.
	registry = registry.WithSecrets(openVault(context.Background(), stateDir))
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

	// Destino dos avisos: uma fila em disco, no volume durável.
	//
	// Ele GRAVA e retorna — não envia nada. Quem entrega é `agentd
	// -notify-drain`, um processo separado chamado por timer. É essa separação
	// que faz o aviso sobreviver à queda da sessão que iniciou a tarefa: no
	// projeto anterior, o transporte de saída disputava a conexão de entrada, e o
	// agendador precisava DERRUBAR o serviço para conseguir falar.
	//
	// Publicar é escrita local, então não pode travar a tarefa esperando um
	// serviço remoto responder — e a tarefa está segurando a trava da tela.
	eventSpool, err := events.NewSpool(stateDir + "/events")
	if err != nil {
		return err
	}
	// Só o que PEDE AÇÃO é enfileirado. Avisar de tudo ensina quem recebe a
	// ignorar, inclusive o pedido de take-over — o único que trava a tela até
	// alguém agir.
	eventSink := events.OnlyKinds(eventSpool, domain.EventBlocked, domain.EventFailed)

	// Pelo CLI o diário também vale: é a mesma máquina, e uma lição aprendida
	// numa tarefa de linha de comando serve às da porta HTTP igual.
	//
	// `WithTaskBudget` fica de FORA aqui de propósito: o CLI não tem teto de
	// tempo, e inventar um mudaria o comportamento de um caminho que ninguém
	// pediu para mudar.
	taskJournal := journal.New(stateDir, time.Now, maxGuardrailsBytes)

	// A tabela de preços vale no CLI também: é a mesma máquina e a mesma conta,
	// e um teto que só existisse pela porta HTTP seria surpresa.
	priceTable, priceErr := pricing.Load(filepath.Join(stateDir, "pricing.json"))
	if priceErr != nil {
		fmt.Fprintf(os.Stderr, "aviso: tabela de preços ignorada, teto de custo desligado: %v\n", priceErr)
		priceTable, _ = pricing.Parse([]byte("{}"))
	}
	effectiveModel := modelName
	if effectiveModel == "" {
		effectiveModel = xai.DefaultModel()
	}

	agent := service.NewAgent(languageModel, toolset, screenDriver, taskStore, screenLock, time.Now, agentInstructions,
		service.WithEventSink(eventSink),
		service.WithGuardrailJournal(taskJournal),
		service.WithCostEstimator(priceTable, effectiveModel))

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

// abandonTask desiste de uma tarefa e libera a tela.
//
// Delega ao Lifecycle do serviço, que é o MESMO código usado pela porta HTTP.
// Duplicar as regras aqui é como as duas pontas divergem, e a que diverge em
// silêncio é sempre a que ninguém roda — foi assim que o abandono de tarefa
// pendente passou a mentir sobre ter liberado a tela.
func abandonTask(ctx context.Context, taskStore *store.FileStore, screenDriver *screen.XScreen, taskID string) error {
	if taskID == "" {
		return errors.New("informe a tarefa com -task")
	}
	task, err := service.NewLifecycle(taskStore, screenDriver, time.Now).Abandon(ctx, taskID)
	if err != nil {
		return err
	}
	fmt.Printf("tarefa %s abandonada; tela %d liberada\n", task.ID, task.Screen)
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
