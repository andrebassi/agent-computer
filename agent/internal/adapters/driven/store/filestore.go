// Package store persiste tarefas e conversas em arquivo, dentro do volume
// durável.
//
// A escolha de arquivo em vez de banco é deliberada: o estado precisa
// sobreviver à reconstrução do computador, e um diretório no volume atravessa
// o rebuild sem serviço nenhum precisar subir junto.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// FileStore guarda cada tarefa e cada conversa num arquivo JSON.
type FileStore struct {
	root string
}

// persistedConversation é o formato em disco da conversa.
//
// Existe separado de domain.Conversation porque o campo de segredos daquele
// tipo não é exportado, de propósito — e é justamente o que não pode ser
// gravado. Um struct próprio torna impossível serializá-lo por descuido.
type persistedConversation struct {
	TaskID   string           `json:"task_id"`
	Messages []domain.Message `json:"messages"`
}

// NewFileStore cria o diretório de estado se ele não existir.
func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Join(root, "tasks"), 0o755); err != nil {
		return nil, fmt.Errorf("criando diretório de tarefas: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "conversations"), 0o755); err != nil {
		return nil, fmt.Errorf("criando diretório de conversas: %w", err)
	}
	return &FileStore{root: root}, nil
}

// SaveTask grava a tarefa.
func (s *FileStore) SaveTask(_ context.Context, task *domain.Task) error {
	return writeJSON(filepath.Join(s.root, "tasks", task.ID+".json"), task)
}

// LoadTask lê uma tarefa. Devolve nil sem erro quando ela não existe: quem
// chama trata ausência como caso normal, não como falha.
func (s *FileStore) LoadTask(_ context.Context, id string) (*domain.Task, error) {
	var task domain.Task
	ok, err := readJSON(filepath.Join(s.root, "tasks", id+".json"), &task)
	if err != nil || !ok {
		return nil, err
	}
	return &task, nil
}

// ListActiveTasks devolve todas as tarefas que ainda ocupam alguma tela.
//
// Varredura linear é adequada aqui: o diretório guarda dezenas de tarefas, não
// milhões, e um índice seria mais uma coisa a ficar dessincronizada do estado
// real depois de um rebuild.
//
// Enumerar é o que torna a reconciliação possível. Sem isto, um processo morto
// deixa tarefa presa numa tela e não há como descobrir QUAIS — e uma varredura
// por tela só enxerga a primeira de cada uma, escondendo a segunda até a
// primeira sair.
func (s *FileStore) ListActiveTasks(_ context.Context) ([]*domain.Task, error) {
	dir := filepath.Join(s.root, "tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var active []*domain.Task
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var task domain.Task
		ok, err := readJSON(filepath.Join(dir, e.Name()), &task)
		if err != nil || !ok {
			// Arquivo corrompido não pode travar a busca inteira: seria um
			// arquivo ruim impedindo qualquer tarefa nova de começar.
			continue
		}
		if task.Active() {
			copia := task
			active = append(active, &copia)
		}
	}
	return active, nil
}

// ActiveTaskOnScreen devolve a tarefa que ocupa a tela, ou nil.
func (s *FileStore) ActiveTaskOnScreen(ctx context.Context, screen int) (*domain.Task, error) {
	active, err := s.ListActiveTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range active {
		if task.Screen == screen {
			return task, nil
		}
	}
	return nil, nil
}

// SaveConversation grava o histórico, sem os segredos rastreados.
func (s *FileStore) SaveConversation(_ context.Context, c *domain.Conversation) error {
	p := persistedConversation{TaskID: c.TaskID, Messages: c.Messages}
	return writeJSON(filepath.Join(s.root, "conversations", c.TaskID+".json"), p)
}

// LoadConversation lê o histórico. Devolve nil sem erro quando não existe.
func (s *FileStore) LoadConversation(_ context.Context, taskID string) (*domain.Conversation, error) {
	var p persistedConversation
	ok, err := readJSON(filepath.Join(s.root, "conversations", taskID+".json"), &p)
	if err != nil || !ok {
		return nil, err
	}
	c := &domain.Conversation{TaskID: p.TaskID, Messages: p.Messages}
	return c, nil
}

// writeJSON grava de forma atômica: escreve num temporário e renomeia.
//
// Sem isso, uma queda no meio da escrita deixa um JSON pela metade, e a tarefa
// fica ilegível justamente no momento em que se quer saber o que aconteceu.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("serializando: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("gravando temporário: %w", err)
	}
	return os.Rename(tmp, path)
}

// readJSON lê um arquivo JSON. O booleano diz se ele existia.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("decodificando %s: %w", path, err)
	}
	return true, nil
}
