package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/telemetry"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/tools"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/vault"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/verifier"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/xai"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driving/api"
	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// deps são as dependências CARAS, montadas uma vez e compartilhadas por todas as
// tarefas.
//
// O que varia por tarefa — o conjunto de ferramentas — fica na fábrica, porque
// depende do texto do pedido.
// maxGuardrailsBytes é o teto do arquivo de lições, em bytes.
//
// O arquivo entra no prompt de sistema de toda tarefa. 4 KB cabe algumas dezenas
// de lições e custa pouco perto do histórico, que vai a centenas de KB.
const maxGuardrailsBytes = 4096

// agentVersion identifica a build no recurso da telemetria.
//
// Existe para que um trecho estranho no backend possa ser atribuído a uma versão
// do binário: sem isso, "isso começou depois do último deploy" é palpite, e o
// deploy aqui é manual e frequente. Sobe a cada mudança que valha diferenciar
// no gráfico -- não a cada commit.
const agentVersion = "0.1.0"

type deps struct {
	model    ports.LanguageModel
	store    *store.FileStore
	screen   *screen.XScreen
	lock     *lock.FileLock
	registry *connectors.Registry
	skills   *skills.Store
	sink     ports.EventSink
	stateDir string
	verbose  bool
	// journal escreve os quatro arquivos de memória e devolve as lições que
	// entram no prompt.
	journal *journal.Journal
	// runners é o catálogo fechado de agentes de código para a delegação.
	runners *runners.Catalog
	// prices converte tokens em dólares, para o teto de custo.
	prices *pricing.Table
	// modelName é a chave da tabela de preços.
	modelName string
	// taskBudget é quanto tempo uma tarefa tem, para o detector de tempo de
	// parede saber a FRAÇÃO consumida. Zero desliga o detector.
	taskBudget time.Duration
	// tracer observa o percurso da tarefa. Nil quando não há endpoint
	// configurado, e nesse caso o serviço usa o rastreador mudo dele.
	tracer service.Tracer
	// meter registra as medidas agregáveis. Nil sem endpoint configurado.
	meter service.Meter
	// verifier pergunta se a tarefa foi cumprida antes de deixá-la terminar.
	//
	// Nil por padrão — e nesse caso o serviço usa o verificador mudo dele, que
	// aprova sem consultar nada. Só é montado com AGENTD_VERIFY_COMPLETION=1.
	verifier service.CompletionVerifier
	// flushMetrics esvazia as métricas pendentes antes de o processo sair.
	//
	// SEPARADO do flush de trechos porque os dois provedores do OpenTelemetry
	// são independentes: encerrar um não esvazia o outro, e o esquecido perde
	// exatamente a última janela — que num encerramento é a que explica por que
	// ele aconteceu.
	flushMetrics telemetry.Shutdown
	// flushTelemetry esvazia a fila de trechos antes de o processo sair.
	//
	// Nunca é nil: sem endpoint, é um no-op. Isso evita a checagem em cada
	// caminho de saída — e é justamente num caminho de saída que a checagem
	// esquecida vira pânico.
	flushTelemetry telemetry.Shutdown
}

// buildDeps monta o que não depende do texto da tarefa.
//
// `needsModel` é falso para as operações locais — abandonar, gerenciar catálogo,
// drenar avisos —, que hoje já rodam sem a chave da API de propósito. O servidor
// não pode perder essa propriedade: exigir a chave para liberar uma tela é
// exigi-la justamente quando algo deu errado.
func buildDeps(stateDir, modelName string, needsModel, verbose bool) (*deps, error) {
	d := &deps{stateDir: stateDir, verbose: verbose}

	// A chave da tabela de preços é o modelo EFETIVO: o que a flag pediu, ou o
	// padrão do cliente. Deixar vazio faria a busca falhar e o teto sumir sem
	// aviso, no caminho mais comum — o de quem não passa `-model`.
	d.modelName = modelName
	if d.modelName == "" {
		d.modelName = xai.DefaultModel()
	}

	// O diário e o catálogo são montados SEMPRE, inclusive nas operações locais:
	// nenhum dos dois precisa da chave do modelo, e um `-catalog` que não
	// registrasse atividade deixaria buraco no histórico da máquina.
	d.journal = journal.New(stateDir, time.Now, maxGuardrailsBytes)

	catalog, catalogErr := runners.Load(filepath.Join(stateDir, "runners.json"))
	if catalogErr != nil {
		// Catálogo quebrado não derruba o processo: sem ele a delegação usa o
		// padrão, que é o comportamento de antes de o catálogo existir. Derrubar
		// aqui tiraria do ar um agente inteiro por causa de uma vírgula.
		fmt.Fprintf(os.Stderr, "aviso: catálogo de runners ignorado: %v\n", catalogErr)
		catalog, _ = runners.Parse([]byte("{}"))
	}
	d.runners = catalog

	// A tabela de preços é lida do volume, e não compilada: preço envelhece, e
	// uma tabela desatualizada dentro do binário só se corrige recompilando.
	//
	// Sem tabela, o agente roda igual e o teto em dólar não existe. Derrubar o
	// processo por falta de preço trocaria um risco financeiro por uma parada
	// certa.
	prices, priceErr := pricing.Load(filepath.Join(stateDir, "pricing.json"))
	if priceErr != nil {
		fmt.Fprintf(os.Stderr, "aviso: tabela de preços ignorada, teto de custo desligado: %v\n", priceErr)
		prices, _ = pricing.Parse([]byte("{}"))
	}
	d.prices = prices

	if needsModel {
		// A chave vem do COFRE e, na falta dele, do ambiente. Nunca de
		// argumento: `ps` mostra a linha de comando de qualquer processo a
		// qualquer usuário da máquina.
		//
		// Este é o caminho do `-serve`, e ele ficou de fora quando o caminho da
		// linha de comando passou a usar o cofre. O sintoma foi a porta HTTP
		// subir e morrer em laço com "XAI_API_KEY não está no ambiente", com a
		// chave gravada no cofre ao lado — o tipo de divergência que só aparece
		// na máquina, porque cada caminho monta as dependências por si.
		apiKey, source, err := resolveModelKey(context.Background(), stateDir)
		if err != nil {
			return nil, err
		}
		if source != vault.SourceVault {
			fmt.Fprintf(os.Stderr, "aviso: chave do modelo veio do %s, não do cofre\n", source)
		}
		options := []xai.Option{}
		if modelName != "" {
			options = append(options, xai.WithModel(modelName))
		}
		model, err := xai.NewClient(apiKey, options...)
		if err != nil {
			return nil, err
		}
		d.model = model
	}

	var err error
	if d.store, err = store.NewFileStore(stateDir); err != nil {
		return nil, err
	}
	if d.screen, err = screen.NewXScreen(stateDir + "/status"); err != nil {
		return nil, err
	}
	if d.lock, err = lock.NewFileLock(stateDir + "/locks"); err != nil {
		return nil, err
	}
	if d.registry, err = connectors.NewRegistry(stateDir + "/connectors"); err != nil {
		return nil, err
	}
	// A credencial de conector também sai do cofre. Ela nunca deixa este
	// processo: o agentd monta a requisição HTTP ele mesmo.
	d.registry = d.registry.WithSecrets(openVault(context.Background(), stateDir))
	if d.skills, err = skills.NewStore(stateDir + "/skills"); err != nil {
		return nil, err
	}

	// Destino dos avisos: fila em disco no volume durável. Ele grava e retorna;
	// quem entrega é `agentd -notify-drain`, processo separado.
	spool, err := events.NewSpool(stateDir + "/events")
	if err != nil {
		return nil, err
	}
	// Só o que PEDE AÇÃO é enfileirado. Avisar de tudo ensina quem recebe a
	// ignorar, inclusive o pedido de take-over.
	d.sink = events.OnlyKinds(spool, domain.EventBlocked, domain.EventFailed)

	// Telemetria por último, e sem poder derrubar nada.
	//
	// O endpoint vem do ambiente porque o valor muda com a máquina que observa,
	// não com o código. Vazio é o caso normal: sem ele o agente roda igual, com
	// o rastreador mudo do serviço.
	//
	// A falha aqui NÃO é fatal, e segue o molde que este arquivo já usa para
	// preço e catálogo de runners: avisa em stderr e continua. Um backend de
	// observação fora do ar não pode impedir o trabalho de acontecer — seria a
	// observação derrubando o observado.
	tracer, flush, telemetryErr := telemetry.New(
		context.Background(),
		os.Getenv("AGENTD_OTLP_ENDPOINT"),
		"agentd",
		agentVersion,
	)
	meter, flushMeter, meterErr := telemetry.NewMeter(
		context.Background(),
		os.Getenv("AGENTD_OTLP_METRICS_ENDPOINT"),
		"agentd",
		agentVersion,
	)
	if meterErr != nil {
		fmt.Fprintf(os.Stderr, "aviso: métricas desligadas: %v\n", meterErr)
		d.flushMetrics = func(context.Context) error { return nil }
	} else {
		if meter != nil {
			d.meter = meter
		}
		d.flushMetrics = flushMeter
	}

	if telemetryErr != nil {
		fmt.Fprintf(os.Stderr, "aviso: telemetria desligada: %v\n", telemetryErr)
		d.flushTelemetry = func(context.Context) error { return nil }
	} else {
		// Atribuição condicional: `telemetry.New` devolve nil quando não há
		// endpoint, e passar um nil tipado para `service.WithTracer` faria a
		// checagem de nil de lá falhar — a interface não seria nil, o ponteiro
		// dentro dela sim, e o pânico viria na primeira chamada.
		if tracer != nil {
			d.tracer = tracer
		}
		d.flushTelemetry = flush
	}

	// Verificação de conclusão, ligada por AGENTD_VERIFY_COMPLETION=1.
	//
	// Desligada por padrão porque muda duas coisas que ninguém pode ganhar só
	// por atualizar o binário: o CUSTO — uma chamada de modelo a mais por
	// tarefa, sobre um histórico resumido — e o DESFECHO, já que a tarefa passa
	// a poder não terminar na primeira parada.
	//
	// Usa o MESMO modelo que executa. Um modelo diferente seria mais
	// independente, mas exigiria segunda credencial e segunda tabela de preço; a
	// contaminação real que preocupa é a de turno (quem acaba de dizer "pronto"
	// confirma), e essa a conversa separada já resolve.
	if os.Getenv("AGENTD_VERIFY_COMPLETION") == "1" {
		if d.model == nil {
			// Operação local (abandonar, catálogo, drenar avisos) não monta
			// modelo. Ligar o verificador aqui daria um ponteiro nil dentro de
			// interface não-nil, e o pânico viria na primeira verificação — no
			// caminho menos exercitado.
			fmt.Fprintln(os.Stderr, "aviso: AGENTD_VERIFY_COMPLETION pedido sem modelo; verificação desligada")
		} else {
			d.verifier = verifier.New(d.model)
			// Dizer que está ligada não é ruído: ela custa uma chamada de modelo
			// por tarefa e pode mudar o desfecho. Sem esta linha, a única forma
			// de saber se a opção pegou é a tarefa se comportar diferente — e
			// foi exatamente essa dúvida que apareceu no primeiro teste na
			// máquina, em 01/09/2026.
			fmt.Fprintln(os.Stderr, "verificação de conclusão LIGADA (custa uma chamada de modelo por tarefa)")
		}
	}

	return d, nil
}

// agentFactory devolve a função que monta o agente de UMA tarefa.
//
// É a mesma usada pela linha de comando e pela porta HTTP. Duplicar esta
// montagem é como as duas entradas divergem — e a que diverge em silêncio é
// sempre a que ninguém roda.
func (d *deps) agentFactory() api.AgentFactory {
	return func(prompt string) (ports.TaskRunner, string, error) {
		// Ferramentas sempre disponíveis, independentes de conector.
		toolset := []ports.Tool{
			tools.NewShellSandboxed("/workspace", toolSandbox()),
			tools.NewTakeover(),
		}
		// Ferramentas de navegador: é o que permite ao agente PILOTAR o Chrome
		// da própria tela. Elas falam com a porta de depuração local, que nunca
		// sai de 127.0.0.1 — a porta dá controle total do navegador, incluindo
		// ler cookie de sessão.
		toolset = append(toolset, tools.NewBrowserTools(d.stateDir+"/screenshots")...)
		// Delegação a um agente de código. Os dois não se substituem: este
		// navega, chama API e sabe parar numa barreira sensível; o outro edita
		// arquivo e mexe em git.
		toolset = append(toolset, tools.NewDelegateSandboxed("/workspace", d.stateDir+"/anthropic.env", toolSandbox(), tools.WithRunners(d.runners)))

		// Conectores e habilidades dependem do TEXTO — é por isso que a
		// montagem é por tarefa, e não uma vez no boot. Um agente montado no
		// boot ignoraria os marcadores em silêncio.
		request := domain.ParseTaskRequest(prompt)
		if len(request.Connectors) > 0 {
			attached, missing := d.registry.ToolsFor(request.Connectors)
			toolset = append(toolset, attached...)
			if len(missing) > 0 && d.verbose {
				// Conector inexistente é aviso, não erro fatal: derrubar a
				// tarefa por um nome trocado é pior do que seguir sem ele.
				fmt.Fprintf(os.Stderr, "aviso: conector(es) não instalado(s): %s\n", strings.Join(missing, ", "))
			}
			if d.verbose {
				fmt.Printf("conectores anexados: %s (%d ferramentas)\n",
					strings.Join(request.Connectors, ", "), len(attached))
			}
		}

		expanded, missingSkills := d.skills.Expand(request.Skills)
		if len(missingSkills) > 0 && d.verbose {
			fmt.Fprintf(os.Stderr, "aviso: habilidade(s) não encontrada(s): %s\n", strings.Join(missingSkills, ", "))
		}
		if expanded != "" && d.verbose {
			fmt.Printf("habilidades aplicadas: %s\n", strings.Join(request.Skills, ", "))
		}

		// O texto segue sem os marcadores: eles são instrução para o agente, não
		// para o modelo. As habilidades entram DEPOIS do pedido, para o objetivo
		// vir primeiro.
		finalPrompt := request.Prompt + expanded

		agent := service.NewAgent(d.model, toolset, d.screen, d.store, d.lock,
			time.Now, agentInstructions,
			service.WithEventSink(d.sink),
			service.WithGuardrailJournal(d.journal),
			service.WithTaskBudget(d.taskBudget),
			service.WithCostEstimator(d.prices, d.modelName),
			service.WithTracer(d.tracer),
			service.WithMeter(d.meter),
			// Verificação de conclusão: pergunta se o pedido foi cumprido antes
			// de deixar a tarefa terminar.
			//
			// Só entra quando `d.verifier` existe, e ele só existe com
			// AGENTD_VERIFY_COMPLETION=1. É opção porque muda duas coisas que
			// ninguém pode ganhar por atualizar o binário: o custo (uma chamada
			// de modelo por tarefa) e o desfecho (a tarefa pode não terminar na
			// primeira parada).
			service.WithVerifier(d.verifier),
			// Arma a redação com os segredos dos conectores ANEXADOS. Sem isto o
			// mecanismo existia inteiro e percorria uma lista vazia.
			service.WithTrackedSecrets(d.registry.SecretsFor(context.Background(), request.Connectors)))
		return agent, finalPrompt, nil
	}
}
