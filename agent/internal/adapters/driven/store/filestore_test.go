package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// storeTime é um instante fixo, para as asserções não dependerem do relógio.
var storeTime = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

// newStore cria um armazenamento num diretório temporário do teste.
func newStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore falhou: %v", err)
	}
	return s
}

// Ida e volta de uma tarefa: o que foi gravado precisa voltar igual, porque é
// disso que depende retomar depois de um rebuild do computador.
func TestSaveAndLoadTask(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	task, err := domain.NewTask("t1", 2, "faça algo", storeTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := task.Start(storeTime); err != nil {
		t.Fatalf("start falhou: %v", err)
	}
	if err := s.SaveTask(ctx, task); err != nil {
		t.Fatalf("SaveTask falhou: %v", err)
	}

	got, err := s.LoadTask(ctx, "t1")
	if err != nil {
		t.Fatalf("LoadTask falhou: %v", err)
	}
	if got == nil {
		t.Fatal("tarefa gravada não voltou")
	}
	if got.Screen != 2 || got.State != domain.StateRunning || got.Prompt != "faça algo" {
		t.Fatalf("tarefa voltou diferente: %+v", got)
	}
}

// Tarefa inexistente devolve nil sem erro: ausência é caso normal, não falha.
func TestLoadMissingTaskReturnsNilWithoutError(t *testing.T) {
	got, err := newStore(t).LoadTask(context.Background(), "nao-existe")
	if err != nil {
		t.Fatalf("ausência não devia ser erro: %v", err)
	}
	if got != nil {
		t.Fatalf("esperava nil, veio %+v", got)
	}
}

// A busca por tarefa ativa é a base da trava de uma tarefa por tela.
func TestActiveTaskOnScreenFindsOnlyActiveOnes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	finished, err := domain.NewTask("t-done", 1, "antiga", storeTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := finished.Start(storeTime); err != nil {
		t.Fatalf("start falhou: %v", err)
	}
	if err := finished.Finish(storeTime); err != nil {
		t.Fatalf("finish falhou: %v", err)
	}
	if err := s.SaveTask(ctx, finished); err != nil {
		t.Fatalf("SaveTask falhou: %v", err)
	}

	// Só a concluída existe: a tela precisa estar livre.
	got, err := s.ActiveTaskOnScreen(ctx, 1)
	if err != nil {
		t.Fatalf("ActiveTaskOnScreen falhou: %v", err)
	}
	if got != nil {
		t.Fatalf("tarefa concluída não devia ocupar a tela: %+v", got)
	}

	pending, err := domain.NewTask("t-run", 1, "atual", storeTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := s.SaveTask(ctx, pending); err != nil {
		t.Fatalf("SaveTask falhou: %v", err)
	}
	got, err = s.ActiveTaskOnScreen(ctx, 1)
	if err != nil {
		t.Fatalf("ActiveTaskOnScreen falhou: %v", err)
	}
	if got == nil || got.ID != "t-run" {
		t.Fatalf("devia achar a tarefa ativa, veio %+v", got)
	}
}

// Tarefa de outra tela não conta: as telas são independentes quanto à trava.
func TestActiveTaskOnScreenIgnoresOtherScreens(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	task, err := domain.NewTask("t1", 2, "faça algo", storeTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := s.SaveTask(ctx, task); err != nil {
		t.Fatalf("SaveTask falhou: %v", err)
	}
	got, err := s.ActiveTaskOnScreen(ctx, 1)
	if err != nil {
		t.Fatalf("ActiveTaskOnScreen falhou: %v", err)
	}
	if got != nil {
		t.Fatalf("tarefa da tela 2 não devia aparecer na busca da tela 1: %+v", got)
	}
}

// Um arquivo corrompido não pode travar a busca inteira: seria um JSON ruim
// impedindo qualquer tarefa nova de começar em qualquer tela.
func TestActiveTaskOnScreenSkipsCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore falhou: %v", err)
	}
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(dir, "tasks", "quebrado.json"), []byte("{isso não é json"), 0o644); err != nil {
		t.Fatalf("escrita falhou: %v", err)
	}
	valid, err := domain.NewTask("t-ok", 1, "faça algo", storeTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := s.SaveTask(ctx, valid); err != nil {
		t.Fatalf("SaveTask falhou: %v", err)
	}

	got, err := s.ActiveTaskOnScreen(ctx, 1)
	if err != nil {
		t.Fatalf("arquivo corrompido não devia virar erro: %v", err)
	}
	if got == nil || got.ID != "t-ok" {
		t.Fatalf("a tarefa válida devia ser encontrada mesmo assim, veio %+v", got)
	}
}

// Diretório de tarefas ausente não é erro: significa apenas que nenhuma tarefa
// existiu ainda neste computador.
func TestActiveTaskOnScreenHandlesMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore falhou: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "tasks")); err != nil {
		t.Fatalf("remoção falhou: %v", err)
	}
	got, err := s.ActiveTaskOnScreen(context.Background(), 1)
	if err != nil {
		t.Fatalf("diretório ausente não devia ser erro: %v", err)
	}
	if got != nil {
		t.Fatalf("esperava nil, veio %+v", got)
	}
}

// Conversa vai e volta preservando os turnos e as chamadas de ferramenta.
func TestSaveAndLoadConversation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	conv := domain.NewConversation("t1", "instruções")
	conv.AddUser("faça algo")
	conv.AddAssistant("vou fazer", []domain.ToolCall{{ID: "c1", Name: "shell", Arguments: "{}"}})
	if err := conv.AddToolResult("c1", "saída"); err != nil {
		t.Fatalf("AddToolResult falhou: %v", err)
	}
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation falhou: %v", err)
	}

	got, err := s.LoadConversation(ctx, "t1")
	if err != nil {
		t.Fatalf("LoadConversation falhou: %v", err)
	}
	if got == nil || len(got.Messages) != len(conv.Messages) {
		t.Fatalf("conversa voltou diferente: %+v", got)
	}
	if len(got.Messages[2].ToolCalls) != 1 {
		t.Fatal("as chamadas de ferramenta deviam sobreviver ao disco")
	}
}

// Conversa inexistente devolve nil sem erro, para o agente começar uma nova.
func TestLoadMissingConversationReturnsNil(t *testing.T) {
	got, err := newStore(t).LoadConversation(context.Background(), "nao-existe")
	if err != nil {
		t.Fatalf("ausência não devia ser erro: %v", err)
	}
	if got != nil {
		t.Fatalf("esperava nil, veio %+v", got)
	}
}

// JSON corrompido na leitura direta precisa devolver erro claro, e não um
// objeto meio preenchido que o agente usaria sem perceber.
func TestLoadTaskFailsLoudlyOnCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore falhou: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks", "t1.json"), []byte("{quebrado"), 0o644); err != nil {
		t.Fatalf("escrita falhou: %v", err)
	}
	if _, err := s.LoadTask(context.Background(), "t1"); err == nil {
		t.Fatal("JSON corrompido devia produzir erro")
	}
}

// A gravação é atômica: temporário e rename. Um arquivo .tmp deixado para trás
// indicaria escrita interrompida.
func TestWriteIsAtomicAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore falhou: %v", err)
	}
	task, err := domain.NewTask("t1", 1, "faça algo", storeTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := s.SaveTask(context.Background(), task); err != nil {
		t.Fatalf("SaveTask falhou: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "tasks"))
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("sobrou arquivo temporário: %s", e.Name())
		}
	}
}

// Caminho impossível precisa falhar na construção, e não na primeira gravação.
func TestNewFileStoreFailsOnUnusablePath(t *testing.T) {
	arquivo := filepath.Join(t.TempDir(), "bloqueio")
	if err := os.WriteFile(arquivo, []byte("x"), 0o644); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	if _, err := NewFileStore(filepath.Join(arquivo, "estado")); err == nil {
		t.Fatal("caminho inutilizável devia falhar na construção")
	}
}

// Gravação impossível precisa produzir erro, e não perder o estado em silêncio.
func TestSaveTaskFailsWhenDirectoryDisappears(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore falhou: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remoção falhou: %v", err)
	}
	task, err := domain.NewTask("t1", 1, "faça algo", storeTime)
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := s.SaveTask(context.Background(), task); err == nil {
		t.Fatal("gravação impossível devia falhar")
	}
}

// Conversa corrompida precisa falhar alto: recomeçar em silêncio perderia o
// contexto do trabalho já feito.
func TestLoadConversationFailsOnCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore falhou: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversations", "t1.json"), []byte("{quebrado"), 0o644); err != nil {
		t.Fatalf("escrita falhou: %v", err)
	}
	if _, err := s.LoadConversation(context.Background(), "t1"); err == nil {
		t.Fatal("conversa corrompida devia produzir erro")
	}
}

// Diretório inexistente é criado na construção, senão a primeira gravação
// falharia num computador recém-reconstruído.
func TestNewFileStoreCreatesDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fundo", "do", "poco")
	if _, err := NewFileStore(dir); err != nil {
		t.Fatalf("NewFileStore devia criar a árvore: %v", err)
	}
	for _, sub := range []string{"tasks", "conversations"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Fatalf("diretório %s não foi criado: %v", sub, err)
		}
	}
}
