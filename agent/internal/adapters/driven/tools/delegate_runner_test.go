package tools

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeResolver devolve um comando controlado, sem depender de arquivo JSON.
type fakeResolver struct {
	cmd   []string
	stdin bool
	err   error
	names []string
	// seenPath guarda o caminho de prompt que a ferramenta passou, para o teste
	// conferir que o arquivo — e não o texto — é o que viaja.
	seenPath string
}

// Resolve devolve o comando combinado, ou o erro combinado.
func (f *fakeResolver) Resolve(_, promptPath string) ([]string, bool, error) {
	f.seenPath = promptPath
	if f.err != nil {
		return nil, false, f.err
	}
	out := make([]string, len(f.cmd))
	for i, part := range f.cmd {
		out[i] = strings.ReplaceAll(part, "{prompt}", promptPath)
	}
	return out, f.stdin, nil
}

// Names lista os runners do dublê.
func (f *fakeResolver) Names() []string { return f.names }

// newRunnerDelegate monta a delegação com um resolvedor de mentira.
func newRunnerDelegate(t *testing.T, resolver RunnerResolver) *Delegate {
	t.Helper()
	dir := t.TempDir()
	envFile := dir + "/anthropic.env"
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatalf("preparando credencial: %v", err)
	}
	// Sandbox vazio: o rebaixamento não é o que este arquivo testa, e exigir
	// sudo tornaria o teste dependente da máquina.
	return NewDelegateSandboxed(dir, envFile, NewSandbox(""), WithRunners(resolver))
}

// Sem catálogo, pedir runner explica o que fazer em vez de falhar seco.
func TestRunnerWithoutCatalogExplainsTheDefault(t *testing.T) {
	d := newRunnerDelegate(t, nil)
	result, err := d.Execute(context.Background(), 1, `{"task":"x","runner":"codex"}`)
	if err != nil {
		t.Fatalf("não devia devolver erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("devia ser marcado como falha")
	}
	if !strings.Contains(result.Output, "catálogo") {
		t.Errorf("a mensagem devia citar o catálogo: %s", result.Output)
	}
}

// Recusa do catálogo volta como TEXTO ao modelo, não como erro de execução.
//
// A diferença importa: erro derruba o turno; texto entra no histórico e o modelo
// se corrige sozinho — e a mensagem do catálogo já traz a lista dos que existem.
func TestCatalogRefusalComesBackAsToolText(t *testing.T) {
	resolver := &fakeResolver{err: errors.New("runner não está no catálogo: \"kiro\"; disponíveis: claude")}
	d := newRunnerDelegate(t, resolver)

	result, err := d.Execute(context.Background(), 1, `{"task":"x","runner":"kiro"}`)
	if err != nil {
		t.Fatalf("recusa não devia virar erro de execução: %v", err)
	}
	if !result.Failed {
		t.Error("devia estar marcado como falha")
	}
	if !strings.Contains(result.Output, "disponíveis: claude") {
		t.Errorf("a lista devia chegar ao modelo: %s", result.Output)
	}
}

// O prompt viaja em ARQUIVO, e o arquivo some depois.
//
// Argumento de linha de comando é visível em `ps` para qualquer usuário da
// máquina — inclusive o usuário do modelo, de quem se quer separar o conteúdo.
// E tarefa de código é longa: passar por argumento estoura o limite do sistema.
func TestPromptTravelsInAFileThatIsCleanedUp(t *testing.T) {
	resolver := &fakeResolver{cmd: []string{"echo", "{prompt}"}}
	d := newRunnerDelegate(t, resolver)

	tarefa := "refatore o pacote X e rode os testes"
	result, err := d.Execute(context.Background(), 1,
		`{"task":"`+tarefa+`","runner":"eco"}`)
	if err != nil {
		t.Fatalf("execução: %v", err)
	}
	if result.Failed {
		t.Fatalf("devia ter dado certo: %s", result.Output)
	}
	if resolver.seenPath == "" {
		t.Fatal("o resolvedor devia ter recebido um caminho de prompt")
	}
	if strings.Contains(resolver.seenPath, tarefa) {
		t.Error("o caminho não pode conter o texto da tarefa")
	}
	if _, err := os.Stat(resolver.seenPath); !os.IsNotExist(err) {
		t.Errorf("o arquivo de prompt devia ter sido removido: %v", err)
	}
}

// A saída do runner volta ao modelo, nomeando qual runner rodou.
func TestRunnerOutputIsReturnedNamingTheRunner(t *testing.T) {
	resolver := &fakeResolver{cmd: []string{"echo", "terminei-a-tarefa"}}
	d := newRunnerDelegate(t, resolver)

	result, err := d.Execute(context.Background(), 1, `{"task":"x","runner":"eco"}`)
	if err != nil {
		t.Fatalf("execução: %v", err)
	}
	if !strings.Contains(result.Output, "terminei-a-tarefa") {
		t.Errorf("a saída do runner devia voltar: %s", result.Output)
	}
	if !strings.Contains(result.Output, "eco") {
		t.Errorf("o nome do runner devia aparecer: %s", result.Output)
	}
}

// Runner que sai com erro é marcado como falha, com a saída junto.
func TestFailingRunnerIsMarkedAsFailure(t *testing.T) {
	// `false` existe em qualquer Unix e sempre sai diferente de zero.
	resolver := &fakeResolver{cmd: []string{"false"}}
	d := newRunnerDelegate(t, resolver)

	result, err := d.Execute(context.Background(), 1, `{"task":"x","runner":"falso"}`)
	if err != nil {
		t.Fatalf("não devia virar erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("saída diferente de zero devia marcar falha")
	}
	if !strings.Contains(result.Output, "falso") {
		t.Errorf("a mensagem devia nomear o runner: %s", result.Output)
	}
}

// O texto da tarefa chega pela ENTRADA PADRÃO quando o runner pede isso.
func TestStdinRunnerReceivesTheTaskOnStdin(t *testing.T) {
	// `cat` sem argumento copia a entrada padrão para a saída.
	resolver := &fakeResolver{cmd: []string{"cat"}, stdin: true}
	d := newRunnerDelegate(t, resolver)

	result, err := d.Execute(context.Background(), 1, `{"task":"marcador-de-stdin","runner":"gato"}`)
	if err != nil {
		t.Fatalf("execução: %v", err)
	}
	if !strings.Contains(result.Output, "marcador-de-stdin") {
		t.Errorf("a tarefa devia ter chegado pela entrada padrão: %s", result.Output)
	}
}

// Sem `runner`, o caminho alternativo NEM É TOCADO.
//
// É a garantia de que nada do que já estava testado mudou: o resolvedor deste
// dublê registra o caminho de prompt, e ele tem de continuar vazio.
func TestWithoutRunnerTheAlternatePathIsNotUsed(t *testing.T) {
	resolver := &fakeResolver{cmd: []string{"echo", "nao-devia-rodar"}}
	d := newRunnerDelegate(t, resolver)

	// O agente padrão (`claude`) não existe no PATH do teste, então a execução
	// falha — e é isso que se quer: falhar pelo caminho ANTIGO.
	_, _ = d.Execute(context.Background(), 1, `{"task":"x"}`)
	if resolver.seenPath != "" {
		t.Errorf("sem runner, o catálogo não devia ser consultado (veio %q)", resolver.seenPath)
	}
}

// O arquivo de prompt nasce 0600.
func TestPromptFileIsOwnerOnly(t *testing.T) {
	path, cleanup, err := writePromptFile("conteúdo sensível")
	if err != nil {
		t.Fatalf("gravando: %v", err)
	}
	defer cleanup()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissão %o, esperava 600", perm)
	}
	conteudo, _ := os.ReadFile(path)
	if string(conteudo) != "conteúdo sensível" {
		t.Errorf("o conteúdo devia ser preservado: %q", conteudo)
	}
}
