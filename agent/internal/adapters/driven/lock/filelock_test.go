package lock

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// A trava tem de valer entre PROCESSOS, não só entre goroutines: duas
// invocações do agentd na mesma tela são processos distintos, e é esse o caso
// que a documentação proíbe. Um segundo processo é gerado de propósito.
func TestLockIsHeldAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	l, err := NewFileLock(dir)
	if err != nil {
		t.Fatalf("NewFileLock falhou: %v", err)
	}
	release, err := l.Acquire(context.Background(), 1, "t1")
	if err != nil {
		t.Fatalf("primeira aquisição devia funcionar: %v", err)
	}
	defer func() { _ = release() }()

	// flock -n devolve código diferente de zero quando não consegue travar. É o
	// mesmo mecanismo do núcleo que o código usa, então o teste exercita a
	// trava de verdade, e não uma imitação.
	path := filepath.Join(dir, "screen-1.lock")
	if err := exec.Command("flock", "-n", path, "-c", "true").Run(); err == nil {
		t.Fatal("outro processo conseguiu travar a mesma tela — a trava não vale entre processos")
	}
}

// Tela ocupada precisa recusar na hora, com erro reconhecível, e não ficar
// pendurada esperando a outra tarefa acabar.
func TestAcquireFailsFastWhenBusy(t *testing.T) {
	dir := t.TempDir()
	l, err := NewFileLock(dir)
	if err != nil {
		t.Fatalf("NewFileLock falhou: %v", err)
	}
	release, err := l.Acquire(context.Background(), 2, "t1")
	if err != nil {
		t.Fatalf("primeira aquisição falhou: %v", err)
	}
	defer func() { _ = release() }()

	other, err := NewFileLock(dir)
	if err != nil {
		t.Fatalf("NewFileLock falhou: %v", err)
	}
	if _, err := other.Acquire(context.Background(), 2, "t2"); !errors.Is(err, domain.ErrScreenBusy) {
		t.Fatalf("esperava ErrScreenBusy, veio %v", err)
	}
}

// Liberar tem de devolver a tela: sem isso, a primeira tarefa a travar prenderia
// a tela para sempre e só um reboot resolveria.
func TestReleaseFreesTheScreen(t *testing.T) {
	dir := t.TempDir()
	l, err := NewFileLock(dir)
	if err != nil {
		t.Fatalf("NewFileLock falhou: %v", err)
	}
	release, err := l.Acquire(context.Background(), 3, "t1")
	if err != nil {
		t.Fatalf("aquisição falhou: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("liberação falhou: %v", err)
	}
	again, err := l.Acquire(context.Background(), 3, "t2")
	if err != nil {
		t.Fatalf("a tela devia estar livre depois da liberação: %v", err)
	}
	_ = again()
}

// Telas diferentes não disputam entre si.
func TestDifferentScreensDoNotBlockEachOther(t *testing.T) {
	dir := t.TempDir()
	l, err := NewFileLock(dir)
	if err != nil {
		t.Fatalf("NewFileLock falhou: %v", err)
	}
	first, err := l.Acquire(context.Background(), 1, "t1")
	if err != nil {
		t.Fatalf("tela 1 falhou: %v", err)
	}
	defer func() { _ = first() }()
	second, err := l.Acquire(context.Background(), 2, "t2")
	if err != nil {
		t.Fatalf("tela 2 não devia ser bloqueada pela tela 1: %v", err)
	}
	_ = second()
}

// O identificador de quem tomou a trava é gravado para diagnóstico: sem ele, a
// mensagem de tela ocupada não diz qual tarefa está segurando.
func TestLockFileRecordsTheOwner(t *testing.T) {
	dir := t.TempDir()
	l, err := NewFileLock(dir)
	if err != nil {
		t.Fatalf("NewFileLock falhou: %v", err)
	}
	release, err := l.Acquire(context.Background(), 4, "tarefa-abc")
	if err != nil {
		t.Fatalf("aquisição falhou: %v", err)
	}
	defer func() { _ = release() }()

	data, err := os.ReadFile(filepath.Join(dir, "screen-4.lock"))
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if string(data) != "tarefa-abc" {
		t.Fatalf("o arquivo devia registrar o dono, veio %q", string(data))
	}
}

// O diretório é criado na construção, senão a primeira tarefa falharia num
// computador recém-reconstruído.
func TestNewFileLockCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "travas", "aqui")
	if _, err := NewFileLock(dir); err != nil {
		t.Fatalf("NewFileLock devia criar o diretório: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("diretório não foi criado: %v", err)
	}
}
