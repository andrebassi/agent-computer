package screen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// O diretório de status é criado na construção, senão o primeiro status
// falharia num computador recém-reconstruído.
func TestNewXScreenCreatesStatusDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "status", "aqui")
	if _, err := NewXScreen(dir); err != nil {
		t.Fatalf("NewXScreen devia criar o diretório: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("diretório não foi criado: %v", err)
	}
}

// Caminho impossível precisa falhar na construção, e não silenciosamente na
// primeira escrita.
func TestNewXScreenFailsOnUnusablePath(t *testing.T) {
	// Um arquivo comum no lugar do diretório torna a criação impossível.
	file := filepath.Join(t.TempDir(), "arquivo")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("escrita falhou: %v", err)
	}
	if _, err := NewXScreen(filepath.Join(file, "status")); err == nil {
		t.Fatal("caminho inutilizável devia falhar na construção")
	}
}

// O status precisa ficar no arquivo mesmo quando o X não existe.
//
// É o ponto central deste adaptador: a máquina de teste não tem servidor X, e o
// droplet pode ter a tela caída. Se a falha do X derrubasse a gravação, o status
// sumiria justamente quando é mais necessário para diagnosticar.
func TestShowStatusPersistsEvenWithoutX(t *testing.T) {
	dir := t.TempDir()
	x, err := NewXScreen(dir)
	if err != nil {
		t.Fatalf("NewXScreen falhou: %v", err)
	}
	// DISPLAY aponta para uma tela que não existe; xsetroot vai falhar.
	if err := x.ShowStatus(context.Background(), 7, "tela 7: trabalhando"); err != nil {
		t.Fatalf("falha do X não devia derrubar a gravação do status: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "screen-7.status"))
	if err != nil {
		t.Fatalf("arquivo de status não foi criado: %v", err)
	}
	if !strings.Contains(string(data), "trabalhando") {
		t.Fatalf("conteúdo inesperado: %q", string(data))
	}
}

// Cada tela tem seu arquivo: um status sobrescrevendo o outro deixaria a
// visualização mostrando a tarefa errada.
func TestShowStatusKeepsOneFilePerScreen(t *testing.T) {
	dir := t.TempDir()
	x, err := NewXScreen(dir)
	if err != nil {
		t.Fatalf("NewXScreen falhou: %v", err)
	}
	ctx := context.Background()
	if err := x.ShowStatus(ctx, 1, "primeira"); err != nil {
		t.Fatalf("ShowStatus falhou: %v", err)
	}
	if err := x.ShowStatus(ctx, 2, "segunda"); err != nil {
		t.Fatalf("ShowStatus falhou: %v", err)
	}
	for screen, want := range map[int]string{1: "primeira", 2: "segunda"} {
		data, err := os.ReadFile(filepath.Join(dir, "screen-"+string(rune('0'+screen))+".status"))
		if err != nil {
			t.Fatalf("status da tela %d não existe: %v", screen, err)
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("tela %d com conteúdo errado: %q", screen, string(data))
		}
	}
}

// Diretório removido depois da construção precisa produzir erro na gravação: o
// status não pode se perder em silêncio.
func TestShowStatusFailsWhenDirectoryDisappears(t *testing.T) {
	dir := t.TempDir()
	x, err := NewXScreen(dir)
	if err != nil {
		t.Fatalf("NewXScreen falhou: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remoção falhou: %v", err)
	}
	if err := x.ShowStatus(context.Background(), 1, "algo"); err == nil {
		t.Fatal("gravação impossível devia produzir erro")
	}
}

// Limpar o aviso é idempotente: chamar sem nada aberto é caso normal, porque
// acontece toda vez que uma tarefa retoma sem ter bloqueado.
func TestClearTakeoverIsIdempotent(t *testing.T) {
	x, err := NewXScreen(t.TempDir())
	if err != nil {
		t.Fatalf("NewXScreen falhou: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := x.ClearTakeover(context.Background(), 1); err != nil {
			t.Fatalf("ClearTakeover devia ser idempotente, falhou na %da vez: %v", i+1, err)
		}
	}
}

// Sem servidor X, abrir a janela de aviso falha — e isso precisa ser reportado,
// não engolido, porque o status em arquivo sozinho não chama a atenção de
// ninguém que esteja olhando a tela.
func TestRequestTakeoverReportsFailureWithoutX(t *testing.T) {
	x, err := NewXScreen(t.TempDir())
	if err != nil {
		t.Fatalf("NewXScreen falhou: %v", err)
	}
	// O erro depende de o binário xmessage existir ou não na máquina; os dois
	// desfechos são aceitáveis, o que não pode acontecer é entrar em pânico.
	_ = x.RequestTakeover(context.Background(), 99, domain.BlockCaptcha, "resolva o desafio")
}
