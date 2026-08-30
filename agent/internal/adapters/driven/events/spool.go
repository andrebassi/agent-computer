// Package events implementa o porto EventSink: leva fatos da tarefa para fora
// do agente.
//
// O adaptador padrão NÃO envia nada — ele enfileira em disco. Quem entrega é
// outro processo, e é isso que satisfaz o requisito duro: matar a sessão que
// iniciou a tarefa não mata a entrega, porque a entrega nunca esteve nela.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// Spool grava o fato no volume durável e devolve.
//
// É a tradução do padrão que o projeto anterior usava para trabalho de fundo:
// em vez de um canal de saída paralelo, o fato vira uma entrada nova que alguém
// consome depois. Lá isso não foi feito para notificação, e o preço foi derrubar
// o serviço a cada aviso — o transporte de saída disputava a mesma conexão do
// transporte de entrada.
//
// Publish é escrita local, e por isso não pode falhar por causa de um serviço
// remoto fora do ar. É essa propriedade que permite chamá-lo de dentro da tarefa
// sem risco: o pior caso é disco cheio, que já derrubaria a tarefa de qualquer
// forma.
type Spool struct {
	path string
}

// NewSpool prepara o arquivo de fila no diretório dado.
func NewSpool(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("criando diretório de eventos: %w", err)
	}
	return &Spool{path: filepath.Join(dir, "events.jsonl")}, nil
}

// Path devolve o arquivo da fila, para diagnóstico e para o drenador.
func (s *Spool) Path() string { return s.path }

// Publish acrescenta uma linha JSON ao final do arquivo.
//
// O_APPEND é o que torna isto seguro: a escrita vai ao fim mesmo com dois
// processos escrevendo, e nenhuma trunca a do outro. Sem ele, dois agentes em
// telas diferentes se sobrescreveriam.
//
// Uma linha por evento, e não um array JSON, pelo mesmo motivo: array exigiria
// reler, alterar e regravar o arquivo inteiro a cada fato — três operações onde
// uma queda no meio deixa o arquivo corrompido.
func (s *Spool) Publish(_ context.Context, event domain.TaskEvent) error {
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("serializando evento: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("abrindo fila de eventos: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("gravando evento: %w", err)
	}
	return nil
}

// Pending lê os fatos ainda não entregues.
//
// Linha ilegível é PULADA, não fatal: um evento corrompido por uma queda no meio
// da escrita não pode impedir a entrega de todos os outros — e o que interessa
// avisar costuma ser o mais recente.
func (s *Spool) Pending(_ context.Context) ([]domain.TaskEvent, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Nenhum evento ainda: é o estado normal de um computador recém
			// criado, não um erro.
			return nil, nil
		}
		return nil, fmt.Errorf("lendo fila de eventos: %w", err)
	}

	var pending []domain.TaskEvent
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event domain.TaskEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		pending = append(pending, event)
	}
	return pending, nil
}

// Clear esvazia a fila depois da entrega.
//
// Truncar em vez de apagar preserva as permissões e o descritor de quem já tem o
// arquivo aberto — um agente escrevendo no mesmo instante continua escrevendo no
// lugar certo, em vez de num arquivo órfão que ninguém mais lê.
//
// ⚠️ Há uma janela: um evento publicado ENTRE a leitura e o truncamento é
// perdido. Fechá-la exigiria trava de arquivo aqui e no Publish, o que tornaria
// a publicação capaz de bloquear a tarefa — trocando uma perda rara por uma
// pausa em toda tarefa. Ver o drenador, que reduz a janela relendo antes.
func (s *Spool) Clear(_ context.Context) error {
	if err := os.Truncate(s.path, 0); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("limpando fila de eventos: %w", err)
	}
	return nil
}
