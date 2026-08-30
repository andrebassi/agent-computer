// Package ports declara as fronteiras do agente: o que ele oferece para fora
// (inbound) e o que ele exige do mundo (outbound).
//
// Nenhuma implementação mora aqui. É o que permite ao domínio ser testado sem
// rede, sem disco e sem servidor X.
package ports

import (
	"context"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// TaskRunner é o porto de ENTRADA: o que o agente oferece a quem o comanda.
//
// É o primeiro porto deste lado — até aqui só havia portos de saída, porque a
// única entrada era a linha de comando, que fala direto com o serviço.
//
// Existe para o adaptador de entrada não depender do serviço concreto e, no
// teste, para o laço inteiro poder ser substituído por um duplo que responde na
// hora. Sem isto, testar a porta HTTP exigiria rodar o modelo de verdade — e o
// teste voltaria a custar token.
type TaskRunner interface {
	Run(ctx context.Context, task *domain.Task) error
	Resume(ctx context.Context, task *domain.Task, humanNote string) error
}

// ToolSpec descreve uma ferramenta para o modelo: nome, para que serve e o
// esquema JSON dos argumentos.
type ToolSpec struct {
	Name        string
	Description string
	// Schema é o JSON Schema dos parâmetros, como a API espera. Fica como texto
	// cru para não amarrar o domínio ao formato de um fornecedor.
	Schema string
	// Concurrent declara que a ferramenta pode rodar AO MESMO TEMPO que outras
	// do mesmo turno.
	//
	// O padrão é FALSO, e isso é decisão de segurança, não conservadorismo. As
	// ferramentas deste agente disputam recursos com estado: as do navegador
	// falam com a MESMA aba do Chrome, o shell mexe no mesmo /workspace, e o
	// take-over muda o estado da tela. Duas ações simultâneas ali não falham —
	// fazem a coisa errada, em silêncio. É exatamente o modo de falha que
	// motivou a trava de uma tarefa por tela.
	//
	// Quem marca isto como verdadeiro assume três compromissos: não guardar
	// estado entre chamadas, não tocar em recurso compartilhado, e honrar o
	// cancelamento do contexto — sem o último, uma ferramenta presa segura o
	// turno inteiro.
	//
	// O campo não é enviado à API: é decisão de execução, não parte do contrato
	// que o modelo vê.
	Concurrent bool
}

// Completion é a resposta do modelo a um turno.
type Completion struct {
	Content   string
	ToolCalls []domain.ToolCall
	// StopReason diz por que o modelo parou. Interessa distinguir parada por
	// chamada de ferramenta de parada por fim de resposta: confundir as duas faz
	// o laço encerrar antes de executar o que foi pedido.
	StopReason string
	// PromptTokens e CompletionTokens alimentam o controle de custo. Em agente
	// autônomo a inferência costuma custar mais que o servidor, então o número
	// precisa estar à mão, não escondido na fatura.
	PromptTokens     int
	CompletionTokens int
}

// LanguageModel é o porto de saída para o modelo de linguagem.
type LanguageModel interface {
	// Complete envia o histórico e as ferramentas disponíveis, e devolve o
	// próximo passo que o modelo quer dar.
	Complete(ctx context.Context, messages []domain.Message, tools []ToolSpec) (*Completion, error)
}

// ToolResult é o que uma ferramenta devolve ao laço do agente.
type ToolResult struct {
	// Output vai para o histórico e, portanto, para o modelo.
	Output string
	// Failed marca erro de execução. Não interrompe a tarefa: o modelo costuma
	// se recuperar sozinho ao ver a mensagem de erro, e abortar a cada falha
	// tornaria o agente inútil na prática.
	Failed bool
	// BlockRequest, quando presente, é o pedido de take-over. A ferramenta
	// devolve isto em vez de tentar contornar a barreira — que é exatamente o
	// que a documentação manda não fazer.
	BlockRequest *BlockRequest
}

// BlockRequest é o pedido do agente para uma pessoa assumir.
type BlockRequest struct {
	Reason domain.BlockReason
	Detail string
}

// Tool é uma capacidade que o agente pode exercer.
type Tool interface {
	// Spec descreve a ferramenta para o modelo.
	Spec() ToolSpec
	// Execute roda a ferramenta. `arguments` chega como o JSON cru emitido pelo
	// modelo; cada implementação decodifica o próprio formato.
	Execute(ctx context.Context, screen int, arguments string) (*ToolResult, error)
}

// ScreenDriver desenha e controla a tela de um agente.
type ScreenDriver interface {
	// ShowStatus escreve a linha de status sobre a tela. É o "current status"
	// que a documentação pede na visualização.
	ShowStatus(ctx context.Context, screen int, line string) error
	// RequestTakeover destaca visualmente que a tela espera uma pessoa.
	RequestTakeover(ctx context.Context, screen int, reason domain.BlockReason, detail string) error
	// ClearTakeover remove o destaque quando a pessoa devolve o controle.
	ClearTakeover(ctx context.Context, screen int) error
}

// TaskStore guarda tarefas e conversas no volume durável, para sobreviverem à
// reconstrução do computador.
type TaskStore interface {
	SaveTask(ctx context.Context, task *domain.Task) error
	LoadTask(ctx context.Context, id string) (*domain.Task, error)
	// ActiveTaskOnScreen devolve a tarefa que ocupa a tela, ou nil. É a base da
	// trava de uma tarefa por tela.
	ActiveTaskOnScreen(ctx context.Context, screen int) (*domain.Task, error)
	// ListActiveTasks devolve todas as tarefas que ainda ocupam alguma tela.
	//
	// É a base da reconciliação no boot: sem enumerar, um processo morto deixa
	// tarefa presa numa tela e não há como descobrir quais. Uma varredura por
	// tela não basta — ela só enxerga a primeira de cada uma.
	ListActiveTasks(ctx context.Context) ([]*domain.Task, error)
	SaveConversation(ctx context.Context, c *domain.Conversation) error
	LoadConversation(ctx context.Context, taskID string) (*domain.Conversation, error)
}

// ScreenLock impede duas tarefas na mesma tela.
//
// É porto separado do TaskStore de propósito: a trava precisa valer entre
// PROCESSOS diferentes, e um registro em arquivo lido e escrito em dois passos
// não garante isso. A implementação usa trava de arquivo do sistema operacional.
type ScreenLock interface {
	// Acquire tenta travar a tela. Devolve domain.ErrScreenBusy se já estiver
	// tomada, e uma função de liberação em caso de sucesso.
	Acquire(ctx context.Context, screen int, taskID string) (release func() error, err error)
}

// EventSink publica fatos da tarefa para fora do agente.
//
// É porto de SAÍDA e NÃO sabe qual é o canal — chat, webhook, arquivo, todos
// eles. O requisito duro veio de uma lição cara do projeto anterior: a
// publicação não pode depender da conexão que iniciou a tarefa. Uma tarefa
// disparada por SSH que bloqueia depois de a sessão cair precisa avisar do mesmo
// jeito, e lá a única saída tinha sido derrubar o serviço para poder falar.
type EventSink interface {
	// Publish entrega o fato.
	//
	// NUNCA devolve erro que derrube a tarefa: avisar é efeito colateral, e um
	// destino fora do ar não pode transformar tarefa concluída em tarefa
	// falhada. Quem implementa devolve erro para ser REGISTRADO, não propagado.
	Publish(ctx context.Context, event domain.TaskEvent) error
}

// SecretPrompter pede um valor sigiloso diretamente na tela.
//
// O valor devolvido nunca passa pelo modelo: quem chama entrega direto ao
// destino e registra apenas que o segredo foi fornecido.
type SecretPrompter interface {
	Prompt(ctx context.Context, screen int, req *domain.SecretRequest) (string, error)
}
