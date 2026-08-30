package events

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// newEvent monta um fato mínimo para os testes.
func newEvent(id string, kind domain.TaskEventKind) domain.TaskEvent {
	return domain.TaskEvent{
		TaskID: id, Screen: 1, Kind: kind,
		At: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	}
}

// Publicar acrescenta ao FIM, sem truncar o que já estava lá.
//
// Sem O_APPEND, dois agentes em telas diferentes se sobrescreveriam, e o
// desaparecido seria sempre o de quem escreveu primeiro — a tarefa que bloqueou
// há mais tempo, que é justamente a mais urgente.
func TestSpoolAppendsWithoutTruncating(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"t1", "t2", "t3"} {
		if err := spool.Publish(ctx, newEvent(id, domain.EventBlocked)); err != nil {
			t.Fatalf("Publish falhou: %v", err)
		}
	}

	pending, err := spool.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending falhou: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("esperava 3 eventos, veio %d", len(pending))
	}
	// A ordem é a de publicação: quem bloqueou primeiro espera há mais tempo.
	if pending[0].TaskID != "t1" || pending[2].TaskID != "t3" {
		t.Fatalf("ordem errada: %v", pending)
	}
}

// Fila inexistente é o estado NORMAL de um computador recém-criado, não um erro.
func TestPendingOnMissingFileIsNotAnError(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	pending, err := spool.Pending(context.Background())
	if err != nil {
		t.Fatalf("fila vazia não devia ser erro: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("esperava fila vazia, veio %d", len(pending))
	}
}

// Linha corrompida é PULADA, não fatal.
//
// Uma queda no meio de uma escrita não pode impedir a entrega de todos os outros
// avisos — e o que mais interessa entregar costuma ser o mais recente, que vem
// depois da linha quebrada.
func TestCorruptLineDoesNotBlockTheRest(t *testing.T) {
	dir := t.TempDir()
	spool, err := NewSpool(dir)
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	content := `{"TaskID":"t1","Kind":"blocked"}` + "\n" +
		`{ isto não é json` + "\n" +
		`{"TaskID":"t2","Kind":"failed"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	pending, err := spool.Pending(context.Background())
	if err != nil {
		t.Fatalf("linha corrompida não devia ser fatal: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("esperava os 2 eventos legíveis, veio %d", len(pending))
	}
	if pending[1].TaskID != "t2" {
		t.Fatalf("o evento DEPOIS da linha quebrada devia sobreviver: %v", pending)
	}
}

// Limpar esvazia sem apagar o arquivo.
//
// Truncar preserva as permissões e o descritor de quem já o tem aberto: um
// agente escrevendo no mesmo instante continua escrevendo no lugar certo, em vez
// de num arquivo órfão que ninguém mais lê.
func TestClearTruncatesInsteadOfDeleting(t *testing.T) {
	dir := t.TempDir()
	spool, err := NewSpool(dir)
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	ctx := context.Background()
	if err := spool.Publish(ctx, newEvent("t1", domain.EventBlocked)); err != nil {
		t.Fatalf("Publish falhou: %v", err)
	}
	if err := spool.Clear(ctx); err != nil {
		t.Fatalf("Clear falhou: %v", err)
	}

	info, err := os.Stat(spool.Path())
	if err != nil {
		t.Fatalf("o arquivo devia continuar existindo: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("devia estar vazio, tem %d bytes", info.Size())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("a permissão devia ser 0600, veio %o", perm)
	}
	// E continua publicável depois de limpo.
	if err := spool.Publish(ctx, newEvent("t2", domain.EventFailed)); err != nil {
		t.Fatalf("Publish depois do Clear falhou: %v", err)
	}
	pending, _ := spool.Pending(ctx)
	if len(pending) != 1 || pending[0].TaskID != "t2" {
		t.Fatalf("esperava só o evento novo: %v", pending)
	}
}

// Limpar fila que nunca existiu não é erro.
func TestClearOnMissingFileIsNotAnError(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	if err := spool.Clear(context.Background()); err != nil {
		t.Fatalf("limpar fila inexistente não devia falhar: %v", err)
	}
}

// Diretório impossível de criar falha na construção, e não na primeira
// publicação — que seria no meio de uma tarefa, longe da causa.
func TestNewSpoolFailsOnUnusablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "bloqueio")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	if _, err := NewSpool(filepath.Join(blocker, "eventos")); err == nil {
		t.Fatal("caminho inutilizável devia falhar na construção")
	}
}

// Publicar em caminho inacessível devolve erro — para o serviço registrar, não
// para derrubar a tarefa.
func TestPublishReportsWriteFailure(t *testing.T) {
	dir := t.TempDir()
	spool, err := NewSpool(dir)
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	// Um diretório no lugar do arquivo impede a abertura para escrita.
	if err := os.MkdirAll(spool.Path(), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	if err := spool.Publish(context.Background(), newEvent("t1", domain.EventBlocked)); err == nil {
		t.Fatal("escrita impossível devia devolver erro")
	}
}

// Ler fila ilegível devolve erro, em vez de fingir que está vazia.
//
// Fila vazia e fila inacessível pedem ações diferentes: uma é o estado normal, a
// outra é problema de permissão que ninguém descobriria.
func TestPendingReportsReadFailure(t *testing.T) {
	dir := t.TempDir()
	spool, err := NewSpool(dir)
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	if err := os.MkdirAll(spool.Path(), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	if _, err := spool.Pending(context.Background()); err == nil {
		t.Fatal("fila ilegível devia devolver erro")
	}
}

// O que é gravado sobrevive à ida e volta pelo JSON.
//
// Um campo que se perde aqui só apareceria como aviso incompleto no chat de
// alguém, muito longe da causa.
func TestEventSurvivesRoundTrip(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	original := domain.TaskEvent{
		TaskID: "t9", Screen: 3, Kind: domain.EventBlocked,
		Reason: domain.BlockTwoFactor, Detail: "código do app",
		Summary: "parei no segundo fator",
		At:      time.Date(2026, 8, 30, 12, 34, 56, 0, time.UTC),
	}
	ctx := context.Background()
	if err := spool.Publish(ctx, original); err != nil {
		t.Fatalf("Publish falhou: %v", err)
	}
	pending, err := spool.Pending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("leitura falhou: %v / %d", err, len(pending))
	}
	if pending[0] != original {
		t.Fatalf("o fato mudou na ida e volta:\n  antes: %+v\n  depois: %+v", original, pending[0])
	}
}

// Uma linha por evento, e não um array: array exigiria reler, alterar e regravar
// o arquivo inteiro a cada fato, e uma queda no meio corromperia tudo.
func TestSpoolWritesOneLinePerEvent(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool falhou: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := spool.Publish(ctx, newEvent("t", domain.EventFinished)); err != nil {
			t.Fatalf("Publish falhou: %v", err)
		}
	}
	data, err := os.ReadFile(spool.Path())
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 4 {
		t.Fatalf("esperava 4 linhas, veio %d", lines)
	}
}
