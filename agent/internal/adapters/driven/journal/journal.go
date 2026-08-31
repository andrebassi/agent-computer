// Package journal grava os quatro arquivos de memória do agente: progresso,
// lições, atividade e erros.
//
// A ideia vem do ralph (github.com/iannuttall/ralph), e a diferença está no que
// o código faz com eles. Lá, `guardrails.md` é semeado, o modelo é convidado a
// escrever nele, e o prompt recebe o CAMINHO do arquivo — nenhuma linha de
// código lê o conteúdo. A documentação afirma que as lições são "injected into
// context at the start of each iteration"; não são.
//
// Aqui:
//
//   - quem escreve é o SERVIÇO, a partir de detector determinístico;
//   - o modelo nunca escreve — os arquivos são do `agentd`, e o usuário do
//     modelo (`agent`) não tem permissão;
//   - o CONTEÚDO de `guardrails.md` entra no prompt, não o caminho.
package journal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/service"
)

const (
	// fileMode é 0640: dono lê e escreve, grupo lê, mundo nada.
	//
	// O grupo `agent` PRECISA ler — é como o operador confere o que aconteceu
	// sem virar root. Escrever, não: conteúdo que entra no prompt é instrução, e
	// quem controla a própria instrução não está contido. É a mesma razão de
	// `skills/` ser do `agentd`.
	fileMode os.FileMode = 0o640

	// dirMode acompanha o diretório de estado que já existe.
	dirMode os.FileMode = 0o750

	// maxLessonBytes limita UMA lição.
	//
	// Erro de ferramenta traz saída grande junto; sem corte, uma lição sozinha
	// encheria o teto do arquivo e expulsaria todas as outras.
	maxLessonBytes = 400
)

// Journal grava os quatro arquivos.
//
// O mutex serializa as escritas do processo. Não protege contra dois processos
// (o serviço e o CLI podem coexistir), e isso é aceitável aqui: cada escrita é
// um `O_APPEND` de uma linha, que o kernel entrega inteira em arquivo local.
// `guardrails.md` é a exceção, porque é reescrito por inteiro — e ali a escrita
// é atômica, por temporário e `rename`.
type Journal struct {
	mu  sync.Mutex
	dir string
	// now é injetável para o teste não depender do relógio real.
	now func() time.Time
	// maxLessonsBytes é o teto do arquivo de lições, em bytes.
	maxLessonsBytes int
}

// New monta o diário sobre um diretório de estado.
func New(stateDir string, now func() time.Time, maxLessonsBytes int) *Journal {
	if now == nil {
		now = time.Now
	}
	return &Journal{dir: stateDir, now: now, maxLessonsBytes: maxLessonsBytes}
}

// progressPath é o log append-only do desfecho de cada tarefa.
func (j *Journal) progressPath() string { return filepath.Join(j.dir, "progress.md") }

// guardrailsPath é o único dos quatro cujo conteúdo entra no prompt.
func (j *Journal) guardrailsPath() string { return filepath.Join(j.dir, "guardrails.md") }

// activityPath é o log de iteração: ferramenta, duração e tokens.
func (j *Journal) activityPath() string { return filepath.Join(j.dir, "activity.log") }

// errorsPath é o log de falha de ferramenta, com a contagem de repetição.
func (j *Journal) errorsPath() string { return filepath.Join(j.dir, "errors.log") }

// stamp devolve o instante no formato dos logs.
func (j *Journal) stamp() string { return j.now().UTC().Format(time.RFC3339) }

// appendLine acrescenta uma linha a um dos arquivos de log.
//
// Uma linha por escrita, com `O_APPEND`: é o que permite duas escritas
// concorrentes não se cortarem ao meio em arquivo local.
func (j *Journal) appendLine(path, line string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(j.dir, dirMode); err != nil {
		return fmt.Errorf("criando %s: %w", j.dir, err)
	}
	handle, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, fileMode)
	if err != nil {
		return fmt.Errorf("abrindo %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = handle.Close() }()
	// Quebra de linha do conteúdo viraria linha nova no log e quebraria quem lê
	// linha a linha — vira espaço.
	clean := strings.ReplaceAll(strings.TrimSpace(line), "\n", " ")
	if _, err := fmt.Fprintf(handle, "[%s] %s\n", j.stamp(), clean); err != nil {
		return fmt.Errorf("gravando em %s: %w", filepath.Base(path), err)
	}
	return nil
}

// RecordActivity anota uma iteração.
func (j *Journal) RecordActivity(_ context.Context, line string) error {
	return j.appendLine(j.activityPath(), line)
}

// RecordError anota uma falha.
func (j *Journal) RecordError(_ context.Context, line string) error {
	return j.appendLine(j.errorsPath(), line)
}

// RecordProgress anota o desfecho de uma tarefa.
func (j *Journal) RecordProgress(_ context.Context, line string) error {
	return j.appendLine(j.progressPath(), line)
}

// LearnLesson grava uma lição que passará a entrar no prompt.
//
// Duas propriedades que o arquivo precisa ter, e que um `append` simples não dá:
//
//  1. LIÇÃO REPETIDA NÃO DUPLICA. O mesmo detector dispara de novo na semana
//     seguinte, e sem isto o arquivo enche de cópias da mesma frase — que
//     custam contexto em toda iteração de toda tarefa.
//  2. TETO DE BYTES COM DESCARTE DO MAIS ANTIGO. O arquivo entra no prompt
//     inteiro; sem teto ele cresce para sempre e passa a custar mais do que
//     evita.
func (j *Journal) LearnLesson(_ context.Context, kind service.GuardrailKind, lesson string) error {
	lesson = strings.ReplaceAll(strings.TrimSpace(lesson), "\n", " ")
	if lesson == "" {
		return nil
	}
	if len(lesson) > maxLessonBytes {
		lesson = lesson[:maxLessonBytes] + "…"
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(j.dir, dirMode); err != nil {
		return fmt.Errorf("criando %s: %w", j.dir, err)
	}

	existing, err := os.ReadFile(j.guardrailsPath())
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("lendo guardrails.md: %w", err)
	}

	entry := fmt.Sprintf("- [%s] (%s) %s", j.stamp(), kind, lesson)
	lines := keepUnique(splitLessons(string(existing)), lesson, entry)
	lines = trimToBudget(lines, j.maxLessonsBytes)

	body := header + strings.Join(lines, "\n") + "\n"
	return writeAtomic(j.guardrailsPath(), body)
}

// header abre o arquivo de lições.
//
// Diz a origem em uma linha porque alguém vai abrir isto pelo SSH e precisa
// saber, sem procurar código, que o modelo não escreve aqui.
const header = "# Lições aprendidas\n\n" +
	"Escrito pelo serviço, a partir de detector determinístico. O modelo lê (via\n" +
	"prompt) e nunca escreve.\n\n"

// splitLessons devolve só as linhas de lição, descartando o cabeçalho.
func splitLessons(content string) []string {
	out := make([]string, 0, 8)
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- [") {
			out = append(out, line)
		}
	}
	return out
}

// keepUnique acrescenta a entrada nova e remove a versão anterior da MESMA lição.
//
// A comparação é pelo texto da lição, não pela linha inteira: a linha carrega
// carimbo de tempo e nunca seria igual. Reinserir no fim é deliberado — a lição
// que voltou a acontecer é a mais relevante, e é a última a ser descartada pelo
// teto.
func keepUnique(existing []string, lesson, entry string) []string {
	out := make([]string, 0, len(existing)+1)
	for _, line := range existing {
		if !strings.Contains(line, lesson) {
			out = append(out, line)
		}
	}
	return append(out, entry)
}

// trimToBudget descarta as lições mais antigas até caber no teto.
func trimToBudget(lines []string, budget int) []string {
	if budget <= 0 {
		return lines
	}
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	for total > budget && len(lines) > 1 {
		total -= len(lines[0]) + 1
		lines = lines[1:]
	}
	return lines
}

// writeAtomic grava por temporário e `rename`.
//
// O arquivo é lido a cada iteração de cada tarefa. Um `O_TRUNC` seguido de
// escrita tem uma janela em que ele está vazio, e uma leitura nessa janela
// entrega prompt sem lição nenhuma — falha silenciosa, do tipo que ninguém
// percebe porque o agente só fica um pouco pior.
func writeAtomic(path, body string) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".guardrails-*")
	if err != nil {
		return fmt.Errorf("criando temporário: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()

	if _, err := temp.WriteString(body); err != nil {
		_ = temp.Close()
		return fmt.Errorf("gravando temporário: %w", err)
	}
	if err := temp.Chmod(fileMode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("ajustando permissão: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("fechando temporário: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("publicando guardrails.md: %w", err)
	}
	return nil
}

// Lessons devolve o conteúdo que vai para o prompt.
//
// É o oposto do ralph, e é o ponto inteiro: lá o prompt recebe o caminho e pede
// que o modelo leia; aqui o serviço lê e concatena, então a lição chega mesmo
// que o modelo nunca faça uma chamada de leitura.
//
// Arquivo ausente devolve string vazia sem erro: na primeira execução de uma
// máquina nova não há lição nenhuma, e isso é normal, não falha.
func (j *Journal) Lessons() (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	content, err := os.ReadFile(j.guardrailsPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lendo guardrails.md: %w", err)
	}
	lessons := splitLessons(string(content))
	if len(lessons) == 0 {
		return "", nil
	}
	return strings.Join(lessons, "\n"), nil
}
