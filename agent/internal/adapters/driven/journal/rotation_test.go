package journal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotatesWhenOverLimit prova que o arquivo para de crescer sem fim.
//
// Até 31/08/2026 não havia rotação alguma, e os três arquivos cresciam para
// sempre. O teste falha se alguém remover a chamada de rotação.
func TestRotatesWhenOverLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.log")

	// Um arquivo já acima do teto, como estaria depois de meses de uso.
	oversized := strings.Repeat("x", maxFileBytes+1)
	if err := os.WriteFile(path, []byte(oversized), fileMode); err != nil {
		t.Fatalf("preparando o arquivo grande: %v", err)
	}

	journal := New(dir, fixedClock, 4096)
	if err := journal.RecordActivity(context.Background(), "linha nova"); err != nil {
		t.Fatalf("gravando: %v", err)
	}

	// O antigo foi para `.1`, inteiro.
	rotated, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("o arquivo antigo não foi rotacionado: %v", err)
	}
	if rotated.Size() != int64(len(oversized)) {
		t.Errorf("a rotação truncou o arquivo antigo: %d bytes", rotated.Size())
	}

	// E o atual tem só a linha nova — que é o ponto: quem faz `tail` logo depois
	// de rodar algo vê o que acabou de fazer.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("lendo o arquivo atual: %v", err)
	}
	if !strings.Contains(string(current), "linha nova") {
		t.Error("a linha nova não foi para o arquivo atual")
	}
	if len(current) > 200 {
		t.Errorf("o arquivo atual não começou vazio: %d bytes", len(current))
	}
}

// TestDoesNotRotateBelowLimit é o outro sentido da prova.
//
// Uma rotação que acontecesse a cada escrita seria pior que nenhuma: o `.1`
// teria uma linha, o atual teria uma linha, e o histórico sumiria a cada
// gravação — com o log parecendo funcionar.
func TestDoesNotRotateBelowLimit(t *testing.T) {
	dir := t.TempDir()
	journal := New(dir, fixedClock, 4096)

	for i := 0; i < 5; i++ {
		if err := journal.RecordActivity(context.Background(), "linha pequena"); err != nil {
			t.Fatalf("gravando: %v", err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "activity.log.1")); !os.IsNotExist(err) {
		t.Error("rotacionou um arquivo que está muito abaixo do teto")
	}
	content, err := os.ReadFile(filepath.Join(dir, "activity.log"))
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}
	if got := strings.Count(string(content), "linha pequena"); got != 5 {
		t.Errorf("esperava 5 linhas preservadas, achei %d", got)
	}
}

// TestRotationOverwritesPreviousGeneration documenta a decisão de guardar UMA
// geração só.
//
// Não é descuido: guardar N exigiria renomear em cascata e escolher um N, e o
// histórico longo mora no backend de telemetria. O teste existe para que a
// escolha seja explícita, e não uma surpresa para quem procurar o `.2`.
func TestRotationOverwritesPreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "errors.log")
	journal := New(dir, fixedClock, 4096)

	for round := 0; round < 2; round++ {
		if err := os.WriteFile(path, []byte(strings.Repeat("y", maxFileBytes+1)), fileMode); err != nil {
			t.Fatalf("preparando rodada %d: %v", round, err)
		}
		if err := journal.RecordError(context.Background(), "erro"); err != nil {
			t.Fatalf("gravando rodada %d: %v", round, err)
		}
	}

	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Error("apareceu uma segunda geração; a decisão é guardar apenas .1")
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("a geração .1 sumiu: %v", err)
	}
}
