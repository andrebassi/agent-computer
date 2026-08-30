package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/connectors"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/events"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/lock"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/screen"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/skills"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/store"
	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/tools"
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
}

// buildDeps monta o que não depende do texto da tarefa.
//
// `needsModel` é falso para as operações locais — abandonar, gerenciar catálogo,
// drenar avisos —, que hoje já rodam sem a chave da API de propósito. O servidor
// não pode perder essa propriedade: exigir a chave para liberar uma tela é
// exigi-la justamente quando algo deu errado.
func buildDeps(stateDir, modelName string, needsModel, verbose bool) (*deps, error) {
	d := &deps{stateDir: stateDir, verbose: verbose}

	if needsModel {
		// A chave vem só do ambiente, e quem a coloca lá é o wrapper que a lê do
		// cofre. Assim ela nunca aparece em linha de comando, onde `ps` a
		// exporia a qualquer processo da máquina.
		apiKey := os.Getenv("XAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("XAI_API_KEY não está no ambiente")
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
		toolset = append(toolset, tools.NewDelegate("/workspace", d.stateDir+"/anthropic.env"))

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
			time.Now, agentInstructions, service.WithEventSink(d.sink))
		return agent, finalPrompt, nil
	}
}
