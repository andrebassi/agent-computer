// Package shipper entrega os eventos observados ao backend, sem nunca bloquear
// quem os produz.
//
// A separação entre PRODUZIR e ENTREGAR é a decisão central deste pacote, e
// copia a que o `agentd` já usa para os avisos: quem lê o ring buffer do kernel
// jamais toca em rede. Se tocasse, uma rede lenta encheria o buffer do KERNEL,
// e a perda passaria a acontecer no lugar onde ela é invisível.
package shipper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/andrebassi/agent-computer/probe/internal/decode"
	"github.com/andrebassi/agent-computer/probe/internal/sample"
)

// Padrões de operação, todos com o motivo ao lado.
const (
	// requestTimeout é o teto de uma entrega.
	//
	// O backend roda no Mac, que é um laptop e vai estar fechado. Esperar mais
	// que isso não aumenta a chance de sucesso: só segura o lote seguinte.
	requestTimeout = 5 * time.Second

	// flushInterval é de quanto em quanto tempo o lote pendente é enviado.
	//
	// Cinco segundos é curto o bastante para quem está diagnosticando ver o
	// evento aparecer, e longo o bastante para agrupar a rajada de `execve` que
	// um único comando de shell produz.
	flushInterval = 5 * time.Second

	// maxBufferedEvents é o teto do que se guarda esperando entrega.
	//
	// Ao estourar, DESCARTA O MAIS ANTIGO — e aqui a política é o INVERSO da do
	// spool de avisos do `agentd`, de propósito. Lá o mais antigo é preservado
	// porque cada aviso é uma tela travada esperando uma pessoa. Aqui são
	// milhares de eventos de auditoria, e quem investiga uma máquina degradando
	// quer os recentes; guardar o antigo à custa do novo preservaria justamente
	// a parte inútil.
	maxBufferedEvents = 10000
)

// Shipper acumula eventos e os entrega em lote.
type Shipper struct {
	endpoint string
	client   *http.Client

	mutex   sync.Mutex
	pending []payload
	// dropped conta o que foi descartado por buffer cheio.
	//
	// Existe porque perda SILENCIOSA num registro de auditoria é o defeito que
	// este coletor existe para não ter: sem o contador, "o Chrome não executou
	// nada" e "descartei os execs do Chrome" ficam idênticos no registro.
	dropped uint64
	// bootTime converte o relógio monotônico do kernel em hora do mundo.
	bootTime time.Time
}

// payload é o que sai na rede: um objeto por evento, em JSON Lines.
//
// Campos com o nome que o backend espera. `_msg` e `_time` são o que o
// VictoriaLogs usa como mensagem e carimbo; o resto vira campo consultável.
type payload struct {
	Time     string `json:"_time"`
	Message  string `json:"_msg"`
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	PID      uint32 `json:"pid,omitempty"`
	TGID     uint32 `json:"tgid,omitempty"`
	UID      uint32 `json:"uid,omitempty"`
	GID      uint32 `json:"gid,omitempty"`
	CgroupID uint64 `json:"cgroup_id,omitempty"`
	Comm     string `json:"comm,omitempty"`
	Filename string `json:"filename,omitempty"`

	// Campos de conexão, presentes só quando Kind == kindNetwork.
	Destination     string `json:"destination,omitempty"`
	DestinationPort uint16 `json:"destination_port,omitempty"`
	SourceAddress   string `json:"source_address,omitempty"`
	SourcePort      uint16 `json:"source_port,omitempty"`
	// PrivateDestination é o campo que responde à pergunta de segurança: a
	// conexão foi para a rede interna? `SECURITY.md:202-205` admite que a
	// ferramenta de shell alcança essa rede e nada a limita.
	//
	// SEM `omitempty`: um falso aqui é informação, não ausência. Omiti-lo faria
	// "conexão pública" e "campo não preenchido" ficarem idênticos na consulta.
	PrivateDestination bool `json:"private_destination"`

	// Campos de saúde, presentes só quando Kind == kindHealth.
	//
	// `omitempty` em todos: um evento de exec com sete campos de saúde zerados
	// pesaria o dobro na rede e no índice, e cada zero seria indistinguível de
	// uma medição que deu zero.
	CPUPressure    float64 `json:"cpu_pressure,omitempty"`
	MemoryPressure float64 `json:"memory_pressure,omitempty"`
	IOPressure     float64 `json:"io_pressure,omitempty"`
	MemoryUsed     float64 `json:"memory_used_fraction,omitempty"`
	MemAvailableKB uint64  `json:"mem_available_kb,omitempty"`
	SwapFreeKB     uint64  `json:"swap_free_kb,omitempty"`
}

// Tipos de registro, no campo que os separa nas consultas.
//
// Um campo `kind` em vez de dois endpoints: os dois sinais respondem à MESMA
// pergunta em escalas diferentes — a saúde diz QUE a máquina degradou, os execs
// dizem QUEM causou —, e separá-los em fluxos distintos obrigaria a juntá-los
// de novo por horário na hora de investigar.
const (
	kindExec    = "exec"
	kindHealth  = "health"
	kindNetwork = "connect"
)

// New monta o remetente. Endpoint vazio devolve nil, e o chamador segue sem
// entrega — é o modo de diagnóstico local, em que os eventos só são impressos.
func New(endpoint string, bootTime time.Time) *Shipper {
	if endpoint == "" {
		return nil
	}
	return &Shipper{
		endpoint: endpoint,
		client:   &http.Client{Timeout: requestTimeout},
		bootTime: bootTime,
	}
}

// Add enfileira um evento. NUNCA bloqueia, e nunca toca em rede.
//
// É chamada do laço que drena o ring buffer do kernel. Uma única operação lenta
// aqui se propaga para trás até o buffer do kernel, onde a perda deixa de ser
// contável.
func (s *Shipper) Add(event decode.ExecEvent) {
	if s == nil {
		return
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if len(s.pending) >= maxBufferedEvents {
		// Descarta o mais antigo, e conta. Ver o comentário de
		// maxBufferedEvents para por que esta política é o inverso da do spool
		// de avisos.
		s.pending = s.pending[1:]
		s.dropped++
	}
	s.pending = append(s.pending, payload{
		Time:     event.WallClock(s.bootTime).UTC().Format(time.RFC3339Nano),
		Message:  event.Filename,
		Source:   "agent-probe",
		Kind:     kindExec,
		PID:      event.PID,
		TGID:     event.TGID,
		UID:      event.UID,
		GID:      event.GID,
		CgroupID: event.CgroupID,
		Comm:     event.Comm,
		Filename: event.Filename,
	})
}

// AddHealth enfileira uma amostra de saúde da máquina.
//
// Entra na MESMA fila dos execs, e isso é decisão: as duas respondem à mesma
// pergunta em escalas diferentes. Com filas separadas, "a máquina engasgou às
// 14h32 e o que rodava era isto" viraria uma junção manual por horário — que é
// exatamente o trabalho que a telemetria deveria eliminar.
//
// A hora aqui é a do RELÓGIO DO MUNDO, e não o monotônico convertido: a amostra
// é lida agora, no espaço de usuário, então não há relógio de kernel a
// converter — e usar a conversão traria a deriva sem nenhum ganho.
func (s *Shipper) AddHealth(health sample.Health) {
	if s == nil {
		return
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if len(s.pending) >= maxBufferedEvents {
		s.pending = s.pending[1:]
		s.dropped++
	}
	s.pending = append(s.pending, payload{
		Time:   time.Now().UTC().Format(time.RFC3339Nano),
		Source: "agent-probe",
		Kind:   kindHealth,
		Message: fmt.Sprintf("cpu=%.2f mem=%.2f io=%.2f mem_usado=%.0f%%",
			health.CPUPressureAvg10, health.MemoryPressureAvg10,
			health.IOPressureAvg10, health.MemoryUsedFraction()*100),
		CPUPressure:    health.CPUPressureAvg10,
		MemoryPressure: health.MemoryPressureAvg10,
		IOPressure:     health.IOPressureAvg10,
		MemoryUsed:     health.MemoryUsedFraction(),
		MemAvailableKB: health.MemAvailableKB,
		SwapFreeKB:     health.SwapFreeKB,
	})
}

// AddNet enfileira uma conexão TCP de saída.
//
// Mesma fila dos execs, pelo mesmo motivo da saúde: "este processo rodou e logo
// depois discou para cá" é uma frase só, e filas separadas obrigariam a
// remontá-la por horário.
func (s *Shipper) AddNet(event decode.NetEvent) {
	if s == nil {
		return
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if len(s.pending) >= maxBufferedEvents {
		s.pending = s.pending[1:]
		s.dropped++
	}
	s.pending = append(s.pending, payload{
		Time:   event.WallClock(s.bootTime).UTC().Format(time.RFC3339Nano),
		Source: "agent-probe",
		Kind:   kindNetwork,
		Message: fmt.Sprintf("%s -> %s:%d",
			event.Comm, event.Destination, event.DestinationPort),
		PID:                event.PID,
		TGID:               event.TGID,
		UID:                event.UID,
		CgroupID:           event.CgroupID,
		Comm:               event.Comm,
		Destination:        event.Destination.String(),
		DestinationPort:    event.DestinationPort,
		SourcePort:         event.SourcePort,
		PrivateDestination: event.IsPrivateDestination(),
	})
}

// Dropped devolve quantos eventos foram descartados por buffer cheio.
func (s *Shipper) Dropped() uint64 {
	if s == nil {
		return 0
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.dropped
}

// Pending devolve quantos eventos aguardam entrega.
func (s *Shipper) Pending() int {
	if s == nil {
		return 0
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return len(s.pending)
}

// Flush entrega o lote pendente.
//
// O lote só sai da fila quando a entrega CONFIRMA. Falha mantém tudo para a
// próxima tentativa — que é o comportamento certo com o Mac fechado: os eventos
// esperam, e o teto de buffer é quem impede o crescimento sem fim.
func (s *Shipper) Flush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mutex.Lock()
	batch := s.pending
	s.pending = nil
	s.mutex.Unlock()

	if len(batch) == 0 {
		return nil
	}

	if err := s.send(ctx, batch); err != nil {
		// Devolve o lote para o começo da fila, preservando a ordem: os eventos
		// que chegaram durante a tentativa vêm depois destes, e é assim que a
		// sequência do registro se mantém.
		s.mutex.Lock()
		s.pending = append(batch, s.pending...)
		s.mutex.Unlock()
		return err
	}
	return nil
}

// send monta o corpo em JSON Lines e o entrega.
//
// `encoding/json` monta cada linha, e não concatenação de string, e isso é
// requisito de CORREÇÃO, não de estilo: o nome de arquivo vem de um `execve`
// que o modelo controla, e um binário chamado `x"}{"y` ou com quebra de linha
// no nome corromperia um JSON montado à mão — no melhor caso quebrando a
// ingestão, no pior injetando campos forjados no registro de auditoria.
func (s *Shipper) send(ctx context.Context, batch []payload) error {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	for _, item := range batch {
		if err := encoder.Encode(item); err != nil {
			return fmt.Errorf("codificando evento: %w", err)
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, &body)
	if err != nil {
		return fmt.Errorf("montando requisição: %w", err)
	}
	request.Header.Set("Content-Type", "application/stream+json")

	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("entregando %d evento(s): %w", len(batch), err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 300 {
		return fmt.Errorf("backend recusou %d evento(s): HTTP %d", len(batch), response.StatusCode)
	}
	return nil
}

// FlushInterval devolve o intervalo entre entregas, para quem monta o timer.
func FlushInterval() time.Duration { return flushInterval }
