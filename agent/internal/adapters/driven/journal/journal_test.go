package journal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// fixedClock congela o tempo para os carimbos serem previsíveis.
func fixedClock() time.Time {
	return time.Date(2026, 8, 30, 15, 4, 5, 0, time.UTC)
}

// newJournal monta um diário sobre um diretório temporário.
func newJournal(t *testing.T, budget int) (*Journal, string) {
	t.Helper()
	dir := t.TempDir()
	return New(dir, fixedClock, budget), dir
}

// Cada tipo de registro vai para o SEU arquivo.
//
// Sem este caso, um erro de fio que mandasse tudo para o mesmo arquivo passaria
// despercebido — os quatro existem justamente para separar papéis.
func TestEachRecordGoesToItsOwnFile(t *testing.T) {
	j, dir := newJournal(t, 4096)
	ctx := context.Background()

	if err := j.RecordActivity(ctx, "iteracao=1"); err != nil {
		t.Fatalf("atividade: %v", err)
	}
	if err := j.RecordError(ctx, "ferramenta=shell falhou"); err != nil {
		t.Fatalf("erro: %v", err)
	}
	if err := j.RecordProgress(ctx, "tarefa=t1 concluida"); err != nil {
		t.Fatalf("progresso: %v", err)
	}

	casos := map[string]string{
		"activity.log": "iteracao=1",
		"errors.log":   "ferramenta=shell",
		"progress.md":  "concluida",
	}
	for arquivo, esperado := range casos {
		conteudo, err := os.ReadFile(filepath.Join(dir, arquivo))
		if err != nil {
			t.Errorf("lendo %s: %v", arquivo, err)
			continue
		}
		if !strings.Contains(string(conteudo), esperado) {
			t.Errorf("%s devia conter %q: %s", arquivo, esperado, conteudo)
		}
	}
}

// Os arquivos nascem 0640 — grupo lê, mundo nada.
//
// A permissão É o guardrail: o usuário do modelo pertence ao grupo e precisa
// ler para o operador conferir, mas escrever daria a ele controle sobre o
// próprio prompt de contenção.
func TestFilesAreGroupReadableAndNotWorldReadable(t *testing.T) {
	j, dir := newJournal(t, 4096)
	ctx := context.Background()
	_ = j.RecordActivity(ctx, "linha")
	_ = j.LearnLesson(ctx, service.GuardrailToolLoop, "não repita")

	for _, arquivo := range []string{"activity.log", "guardrails.md"} {
		info, err := os.Stat(filepath.Join(dir, arquivo))
		if err != nil {
			t.Fatalf("stat %s: %v", arquivo, err)
		}
		if perm := info.Mode().Perm(); perm != fileMode.Perm() {
			t.Errorf("%s tem permissão %o, esperava %o", arquivo, perm, fileMode.Perm())
		}
	}
}

// Uma lição gravada volta pela leitura que alimenta o prompt.
//
// É o teste central do módulo: no ralph a lição fica num arquivo que ninguém
// lê. Aqui, o que foi aprendido tem de voltar.
func TestLessonWrittenIsLessonRead(t *testing.T) {
	j, _ := newJournal(t, 4096)
	ctx := context.Background()

	if err := j.LearnLesson(ctx, service.GuardrailToolLoop, "curl sem -L não segue redirect"); err != nil {
		t.Fatalf("aprendendo: %v", err)
	}
	lidas, err := j.Lessons()
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}
	if !strings.Contains(lidas, "curl sem -L") {
		t.Fatalf("a lição devia voltar na leitura: %q", lidas)
	}
	if !strings.Contains(lidas, string(service.GuardrailToolLoop)) {
		t.Errorf("a origem devia aparecer: %q", lidas)
	}
}

// Sem arquivo, a leitura devolve vazio SEM erro.
//
// É o estado de uma máquina recém-criada. Tratar como falha faria toda primeira
// tarefa começar com um erro no log.
func TestLessonsOnFreshMachineIsEmptyWithoutError(t *testing.T) {
	j, _ := newJournal(t, 4096)
	lidas, err := j.Lessons()
	if err != nil {
		t.Fatalf("máquina nova não devia dar erro: %v", err)
	}
	if lidas != "" {
		t.Fatalf("devia vir vazio, veio %q", lidas)
	}
}

// A MESMA lição gravada duas vezes aparece UMA.
//
// O detector volta a disparar semanas depois; sem deduplicação o arquivo enche
// de cópias da mesma frase, e cada cópia custa contexto em toda iteração de
// toda tarefa.
func TestRepeatedLessonDoesNotDuplicate(t *testing.T) {
	j, _ := newJournal(t, 4096)
	ctx := context.Background()
	licao := "o site X exige login antes de buscar"

	for i := 0; i < 3; i++ {
		if err := j.LearnLesson(ctx, service.GuardrailToolLoop, licao); err != nil {
			t.Fatalf("aprendendo (%d): %v", i, err)
		}
	}
	lidas, _ := j.Lessons()
	if n := strings.Count(lidas, licao); n != 1 {
		t.Fatalf("a lição devia aparecer 1 vez, apareceu %d:\n%s", n, lidas)
	}
}

// Passando do teto, a lição MAIS ANTIGA sai e a nova fica.
//
// O arquivo inteiro entra no prompt; sem teto ele cresce para sempre e passa a
// custar mais do que evita.
func TestOldestLessonIsDroppedAtBudget(t *testing.T) {
	// 130 bytes é medido, não chutado: cada linha destas sai com ~63 bytes
	// (carimbo + origem + texto), então o teto cabe duas e a terceira força o
	// descarte. Com 220 as três cabiam e o teste reprovava um código correto —
	// alarme falso, que custa igual ao falso verde.
	j, _ := newJournal(t, 130)
	ctx := context.Background()

	for _, licao := range []string{"primeira-licao-antiga", "segunda-licao", "terceira-licao-nova"} {
		if err := j.LearnLesson(ctx, service.GuardrailToolLoop, licao); err != nil {
			t.Fatalf("aprendendo %q: %v", licao, err)
		}
	}
	lidas, _ := j.Lessons()
	if strings.Contains(lidas, "primeira-licao-antiga") {
		t.Errorf("a mais antiga devia ter saído:\n%s", lidas)
	}
	if !strings.Contains(lidas, "terceira-licao-nova") {
		t.Errorf("a mais nova devia ficar:\n%s", lidas)
	}
	if len(lidas) > 130 {
		t.Errorf("passou do teto: %d bytes", len(lidas))
	}
}

// Lição gigante é cortada, e não expulsa as outras sozinha.
func TestHugeLessonIsTruncated(t *testing.T) {
	j, _ := newJournal(t, 4096)
	ctx := context.Background()
	if err := j.LearnLesson(ctx, service.GuardrailToolLoop, strings.Repeat("x", 5000)); err != nil {
		t.Fatalf("aprendendo: %v", err)
	}
	lidas, _ := j.Lessons()
	if len(lidas) > maxLessonBytes+200 {
		t.Fatalf("a lição devia ser cortada, veio com %d bytes", len(lidas))
	}
}

// Lição vazia não vira linha no arquivo.
//
// Dois dos três detectores não têm lição reaproveitável (teto de turnos e tempo
// de parede) e mandam string vazia; sem esta guarda o arquivo encheria de
// marcadores sem conteúdo.
func TestEmptyLessonIsIgnored(t *testing.T) {
	j, _ := newJournal(t, 4096)
	ctx := context.Background()
	if err := j.LearnLesson(ctx, service.GuardrailTurnCap, "   "); err != nil {
		t.Fatalf("não devia falhar: %v", err)
	}
	lidas, _ := j.Lessons()
	if lidas != "" {
		t.Fatalf("lição vazia não devia gravar nada: %q", lidas)
	}
}

// Quebra de linha no conteúdo não quebra o formato do log.
//
// Erro de ferramenta chega com várias linhas; sem o achatamento, quem lê o log
// linha a linha vê registros pela metade.
func TestNewlineInContentDoesNotBreakLogFormat(t *testing.T) {
	j, dir := newJournal(t, 4096)
	if err := j.RecordError(context.Background(), "erro\ncom\nvarias\nlinhas"); err != nil {
		t.Fatalf("gravando: %v", err)
	}
	conteudo, _ := os.ReadFile(filepath.Join(dir, "errors.log"))
	linhas := strings.Split(strings.TrimSpace(string(conteudo)), "\n")
	if len(linhas) != 1 {
		t.Fatalf("devia ser 1 linha, veio %d: %q", len(linhas), conteudo)
	}
}

// O diário satisfaz o porto que o serviço declara.
//
// Sem esta asserção, uma mudança de assinatura no porto só apareceria no ponto
// de composição — longe daqui, e com mensagem pior.
func TestJournalSatisfiesServicePort(t *testing.T) {
	var _ service.GuardrailJournal = New(t.TempDir(), fixedClock, 4096)
}
