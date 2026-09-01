// Package domain concentra as regras do agente que não dependem de nada
// externo: nem HTTP, nem sistema de arquivos, nem o modelo de linguagem.
// Tudo aqui é testável sem rede e sem disco.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// TaskState é o estado de uma tarefa na tela de um agente.
//
// A documentação do Grok Bot define dois limites que viram estado aqui: um
// agente roda uma tarefa por tela de cada vez, e ele pode PEDIR que uma pessoa
// assuma num passo sensível. Sem estado explícito, "pedir ajuda" seria só uma
// mensagem de texto, que nada impede de ser ignorada.
type TaskState string

const (
	// StatePending: aceita, ainda não começou.
	StatePending TaskState = "pending"
	// StateRunning: o agente está agindo na tela.
	StateRunning TaskState = "running"
	// StateBlocked: o agente parou e espera uma pessoa. Só sai daqui por ação
	// humana explícita — é o "take over" da documentação.
	StateBlocked TaskState = "blocked"
	// StateDone: concluída com sucesso.
	StateDone TaskState = "done"
	// StateFailed: terminou em erro.
	StateFailed TaskState = "failed"
)

// BlockReason enumera os motivos pelos quais o agente para e chama a pessoa.
//
// Os CINCO PRIMEIROS são exatamente os que a documentação lista, e não uma
// lista nossa: senha ou passkey, verificação em duas etapas, CAPTCHA, cobrança
// ou verificação de identidade, e site que exige explicitamente uma pessoa.
// Todos descrevem algo que o SITE exige.
//
// O sexto (`guardrail`) é nosso e tem origem oposta — nós é que paramos o
// agente. A separação está anotada na constante, e vale mantê-la: misturar os
// dois faria a tela explicar a causa errada.
type BlockReason string

const (
	BlockPassword        BlockReason = "password"
	BlockTwoFactor       BlockReason = "two_factor"
	BlockCaptcha         BlockReason = "captcha"
	BlockPaymentIdentity BlockReason = "payment_identity"
	BlockHumanRequired   BlockReason = "human_required"

	// BlockGuardrail é o sexto, e é NOSSO — não vem da documentação do produto,
	// ao contrário dos cinco acima.
	//
	// Existe porque os outros cinco descrevem coisas que o SITE exige, e um
	// guardrail é o oposto: nós é que paramos o agente. Reaproveitar
	// `human_required` faria a tela dizer "o site exige uma pessoa" quando o
	// site não exigiu nada — mentira sobre a causa, justamente na hora em que
	// alguém precisa entender por que a tarefa parou.
	//
	// Quem o emite é sempre um detector determinístico, nunca o modelo: ele não
	// tem ferramenta que produza este motivo.
	BlockGuardrail BlockReason = "guardrail"

	// BlockUnverified é o sétimo motivo: o agente PAROU, mas o que foi pedido
	// não foi cumprido, e devolver a lacuna a ele não resolveu.
	//
	// Separado de `guardrail` de propósito, embora os dois sejam nossos. Um
	// guardrail diz "estava indo longe demais" — laço, custo, turnos; este diz
	// "terminou cedo demais". São o oposto um do outro, e quem olha a tela
	// precisa da diferença: no primeiro caso a pergunta é se vale continuar, no
	// segundo é o que ficou faltando.
	//
	// Também nunca vem do modelo: é o verificador quem o emite, depois de
	// esgotar as devoluções.
	BlockUnverified BlockReason = "unverified"
)

// ValidBlockReason diz se o motivo está entre os previstos. Quem escolhe o
// motivo é o modelo, e modelo inventa valor: sem esta validação, um motivo
// desconhecido bloquearia a tarefa sem que a tela soubesse o que pedir.
func ValidBlockReason(r BlockReason) bool {
	switch r {
	case BlockPassword, BlockTwoFactor, BlockCaptcha, BlockPaymentIdentity, BlockHumanRequired,
		BlockGuardrail, BlockUnverified:
		return true
	}
	return false
}

// Description devolve o texto mostrado na tela quando o agente pede ajuda.
// Fica no domínio porque é regra de produto, não de apresentação.
func (r BlockReason) Description() string {
	switch r {
	case BlockPassword:
		return "precisa de senha ou passkey"
	case BlockTwoFactor:
		return "precisa do código de verificação em duas etapas"
	case BlockCaptcha:
		return "precisa resolver um CAPTCHA"
	case BlockPaymentIdentity:
		return "precisa confirmar pagamento ou identidade"
	case BlockHumanRequired:
		return "o site exige uma pessoa"
	case BlockGuardrail:
		return "um limite de segurança foi atingido"
	}
	return "motivo desconhecido"
}

var (
	// ErrInvalidTransition sinaliza mudança de estado que a máquina não permite.
	ErrInvalidTransition = errors.New("transição de estado inválida")
	// ErrScreenBusy é a trava de uma tarefa por tela.
	ErrScreenBusy = errors.New("a tela já tem uma tarefa ativa")
	// ErrInvalidReason cobre BlockReason fora da lista da documentação.
	ErrInvalidReason = errors.New("motivo de bloqueio inválido")
	// ErrInvalidTask marca pedido malformado de criação de tarefa.
	//
	// É sentinela, e não erro anônimo, porque quem recebe precisa distinguir
	// "quem pediu errou" de "nós falhamos" — pela porta HTTP isso é a diferença
	// entre 400 e 500. Comparar a mensagem para decidir seria acoplar o
	// adaptador ao texto, que muda.
	ErrInvalidTask = errors.New("pedido de tarefa inválido")
)

// Task é uma unidade de trabalho numa tela.
type Task struct {
	ID          string
	Screen      int
	Prompt      string
	State       TaskState
	BlockReason BlockReason
	// BlockDetail diz, em uma frase, o que a pessoa precisa fazer.
	BlockDetail string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Failure guarda o motivo quando State é StateFailed.
	Failure string
	// TurnsUsed conta as chamadas ao modelo desta tarefa, ACUMULADAS entre
	// invocações.
	//
	// Mora aqui, e não numa variável do laço, por um defeito medido: o contador
	// de iterações nasce em zero a cada `Run` E a cada `Resume`. Uma tarefa que
	// alterna bloqueio e retomada ganhava 60 turnos novos a cada volta, sem
	// teto nenhum sobre o total. Persistido junto do resto do estado, ele
	// sobrevive à retomada e ao reinício do processo.
	TurnsUsed int
	// CostUSD é quanto a tarefa já custou em inferência, ACUMULADO entre
	// invocações.
	//
	// Mora aqui pela mesma razão de TurnsUsed: sem persistir, uma tarefa que
	// alterna bloqueio e retomada zera a conta a cada volta, e o teto nunca é
	// alcançado por mais que ela gaste.
	//
	// Em dólares, e não em tokens: token não se compara entre modelos, e o
	// limite que importa a quem paga é o da fatura.
	CostUSD float64
	// PromptTokens e CompletionTokens são o acumulado bruto, para o registro.
	//
	// Ficam ao lado do custo porque eles sobrevivem à tabela de preços: se o
	// preço mudar, ou se o modelo não tiver preço, o consumo continua medido.
	PromptTokens     int
	CompletionTokens int
}

// ValidateScreen recusa tela fora do intervalo que existe no systemd.
//
// É exportada, e não uma checagem embutida no `NewTask`, porque quem recebe o
// pedido precisa reprovar a tela ANTES de sondar a trava: a sonda cria o arquivo
// `screen-<n>.lock` e ele fica no disco para sempre. Medido em 31/08/2026, com o
// diretório de travas guardando `screen--1.lock` e `screen-99999999.lock` de
// pedidos que foram corretamente recusados depois — lixo que faz `ls locks/`
// parecer haver mais tarefas do que existem.
func ValidateScreen(screen int) error {
	if screen < 1 || screen > 9 {
		return fmt.Errorf("%w: tela %d fora do intervalo 1..9", ErrInvalidTask, screen)
	}
	return nil
}

// NewTask cria uma tarefa pendente. A tela é validada aqui porque uma tela fora
// do intervalo não existe no systemd, e o erro só apareceria muito depois, na
// forma de um serviço que não sobe.
func NewTask(id string, screen int, prompt string, now time.Time) (*Task, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id da tarefa vazio", ErrInvalidTask)
	}
	if err := ValidateScreen(screen); err != nil {
		return nil, err
	}
	if prompt == "" {
		return nil, fmt.Errorf("%w: prompt vazio", ErrInvalidTask)
	}
	return &Task{
		ID:        id,
		Screen:    screen,
		Prompt:    prompt,
		State:     StatePending,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Active diz se a tarefa ocupa a tela. Bloqueada CONTA como ativa: o agente
// parou, mas a tela e o contexto seguem reservados para ela — é por isso que
// uma tarefa bloqueada impede outra de começar.
func (t *Task) Active() bool {
	return t.State == StatePending || t.State == StateRunning || t.State == StateBlocked
}

// Start move de pendente para rodando.
func (t *Task) Start(now time.Time) error {
	if t.State != StatePending {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.State, StateRunning)
	}
	t.State = StateRunning
	t.UpdatedAt = now
	return nil
}

// Block é o pedido de take-over: o agente para e espera uma pessoa.
func (t *Task) Block(reason BlockReason, detail string, now time.Time) error {
	if t.State != StateRunning {
		return fmt.Errorf("%w: só uma tarefa rodando pode bloquear (estado %s)", ErrInvalidTransition, t.State)
	}
	if !ValidBlockReason(reason) {
		return fmt.Errorf("%w: %q", ErrInvalidReason, reason)
	}
	t.State = StateBlocked
	t.BlockReason = reason
	t.BlockDetail = detail
	t.UpdatedAt = now
	return nil
}

// Resume tira do bloqueio. Só o lado humano chama isto: o agente não tem como
// se desbloquear sozinho, que é o ponto inteiro do take-over.
func (t *Task) Resume(now time.Time) error {
	if t.State != StateBlocked {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.State, StateRunning)
	}
	t.State = StateRunning
	t.BlockReason = ""
	t.BlockDetail = ""
	t.UpdatedAt = now
	return nil
}

// Finish encerra com sucesso.
func (t *Task) Finish(now time.Time) error {
	if t.State != StateRunning {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.State, StateDone)
	}
	t.State = StateDone
	t.UpdatedAt = now
	return nil
}

// Fail encerra em erro. Aceita vir de pendente, rodando ou bloqueado: uma tarefa
// pode ser abandonada enquanto ainda espera a pessoa.
//
// Pendente entra na lista porque uma tarefa que NUNCA CHEGOU A RODAR ainda ocupa
// a tela — Active() a conta. Sem esta transição, abandoná-la não tinha como
// liberar nada, e o comando dizia "tela liberada" sem liberar: o estado ficava
// em pendente e a próxima tarefa levava "a tela já tem uma tarefa ativa".
//
// É também o que permite a reconciliação no boot encerrar uma tarefa pendente
// órfã, deixada por um processo que morreu entre criar e iniciar.
func (t *Task) Fail(reason string, now time.Time) error {
	if t.State != StatePending && t.State != StateRunning && t.State != StateBlocked {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.State, StateFailed)
	}
	t.State = StateFailed
	t.Failure = reason
	t.UpdatedAt = now
	return nil
}

// StatusLine é o texto que o overlay desenha na tela do agente. A documentação
// pede que a visualização mostre "current status", então o status precisa caber
// numa linha legível a distância.
func (t *Task) StatusLine() string {
	switch t.State {
	case StatePending:
		return fmt.Sprintf("tela %d: aguardando início", t.Screen)
	case StateRunning:
		return fmt.Sprintf("tela %d: trabalhando", t.Screen)
	case StateBlocked:
		return fmt.Sprintf("tela %d: PRECISA DE VOCÊ — %s", t.Screen, t.BlockReason.Description())
	case StateDone:
		return fmt.Sprintf("tela %d: concluída", t.Screen)
	case StateFailed:
		return fmt.Sprintf("tela %d: falhou — %s", t.Screen, t.Failure)
	}
	return fmt.Sprintf("tela %d: estado desconhecido", t.Screen)
}
