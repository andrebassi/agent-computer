package domain

import (
	"fmt"
	"time"
)

// TaskEventKind diz o que aconteceu com a tarefa.
type TaskEventKind string

const (
	// EventBlocked é o que mais importa: enquanto ninguém age, a tela fica
	// RESERVADA para essa tarefa e nenhuma outra roda ali. Um aviso perdido não
	// atrasa só uma tarefa — inutiliza a tela.
	EventBlocked TaskEventKind = "blocked"
	// EventFinished: concluída.
	EventFinished TaskEventKind = "finished"
	// EventFailed: terminou em erro.
	EventFailed TaskEventKind = "failed"
)

// TaskEvent é um fato consumado, não um pedido de envio.
//
// Não tem destinatário nem canal, de propósito: quem decide para onde isto vai é
// o adaptador. Se o serviço soubesse o canal, teríamos o acoplamento que o
// projeto de referência pagou caro — lá a saída dependia da conexão de ENTRADA,
// e o agendador precisava DERRUBAR o serviço para conseguir avisar.
type TaskEvent struct {
	TaskID string
	Screen int
	Kind   TaskEventKind
	// Reason só é preenchido quando Kind é EventBlocked.
	Reason BlockReason
	Detail string
	// Summary é a última fala do agente. Pode ser vazia, e vazio significa
	// vazio: ninguém substitui por texto de preenchimento.
	Summary string
	At      time.Time
}

// NewTaskEvent deriva o fato a partir do estado da tarefa.
//
// Devolve false para pendente e rodando: fato só existe quando a tarefa PAROU.
// Publicar "estou rodando" a cada iteração encheria o canal de ruído e — pior —
// ensinaria quem recebe a ignorar as mensagens, inclusive as que pedem ação.
func NewTaskEvent(t *Task, summary string, now time.Time) (TaskEvent, bool) {
	var kind TaskEventKind
	switch t.State {
	case StateBlocked:
		kind = EventBlocked
	case StateDone:
		kind = EventFinished
	case StateFailed:
		kind = EventFailed
	default:
		return TaskEvent{}, false
	}

	detail := t.BlockDetail
	if kind == EventFailed {
		detail = t.Failure
	}
	return TaskEvent{
		TaskID:  t.ID,
		Screen:  t.Screen,
		Kind:    kind,
		Reason:  t.BlockReason,
		Detail:  detail,
		Summary: summary,
		At:      now,
	}, true
}

// Message rende a linha que uma pessoa lê.
//
// Reaproveita Description() do motivo para o texto do aviso e o da tela não
// divergirem: são a mesma informação chegando por dois caminhos, e ler coisas
// diferentes nos dois faria duvidar dos dois.
func (e TaskEvent) Message() string {
	switch e.Kind {
	case EventBlocked:
		text := fmt.Sprintf("tela %d PRECISA DE VOCÊ: %s", e.Screen, e.Reason.Description())
		if e.Detail != "" {
			text += " — " + e.Detail
		}
		return text
	case EventFinished:
		if e.Summary != "" {
			return fmt.Sprintf("tela %d concluiu: %s", e.Screen, e.Summary)
		}
		return fmt.Sprintf("tela %d concluiu", e.Screen)
	case EventFailed:
		if e.Detail != "" {
			return fmt.Sprintf("tela %d falhou: %s", e.Screen, e.Detail)
		}
		return fmt.Sprintf("tela %d falhou", e.Screen)
	default:
		return fmt.Sprintf("tela %d: %s", e.Screen, e.Kind)
	}
}
