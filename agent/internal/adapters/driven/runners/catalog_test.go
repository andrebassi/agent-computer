package runners

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Arquivo ausente devolve catálogo vazio, sem erro.
//
// É o estado da máquina que ainda não recebeu o arquivo novo. Tratar como falha
// quebraria a delegação padrão, que não precisa de catálogo nenhum.
func TestMissingCatalogIsEmptyNotAnError(t *testing.T) {
	catalog, err := Load(filepath.Join(t.TempDir(), "nao-existe.json"))
	if err != nil {
		t.Fatalf("arquivo ausente não devia dar erro: %v", err)
	}
	if len(catalog.Names()) != 0 {
		t.Fatalf("devia vir vazio: %v", catalog.Names())
	}
}

// Comando que invoca shell é RECUSADO na leitura do catálogo.
//
// É a recusa mais importante do pacote. Um `sh -c "..."` devolve exatamente o
// poder que o vetor existe para tirar: com shell no meio voltam `;`, `&&` e
// `$(...)` — e com eles `sudo`, que desfaz o rebaixamento do modelo.
func TestShellCommandIsRejected(t *testing.T) {
	for _, comando := range []string{
		`{"mau":{"cmd":["sh","-c","claude -p"]}}`,
		`{"mau":{"cmd":["/bin/bash","-c","x"]}}`,
		`{"mau":{"cmd":["env","claude"]}}`,
		`{"mau":{"cmd":["/usr/bin/xargs","claude"]}}`,
	} {
		_, err := Parse([]byte(comando))
		if !errors.Is(err, ErrInvalidCatalog) {
			t.Errorf("devia recusar %s, veio %v", comando, err)
		}
	}
}

// Catálogo malformado é recusado com a causa.
func TestMalformedCatalogIsRejected(t *testing.T) {
	casos := map[string]string{
		"json truncado":      `{"claude":`,
		"sem comando":        `{"claude":{"cmd":[]}}`,
		"binário vazio":      `{"claude":{"cmd":["  "]}}`,
		"nome com travessia": `{"../fuga":{"cmd":["claude"]}}`,
		"nome com maiúscula": `{"Claude":{"cmd":["claude"]}}`,
		"campo desconhecido": `{"claude":{"comand":["claude"]}}`,
	}
	for nome, conteudo := range casos {
		if _, err := Parse([]byte(conteudo)); err == nil {
			t.Errorf("%s devia ser recusado", nome)
		}
	}
}

// Runner fora do catálogo é recusado LISTANDO o que existe.
//
// A lista na mensagem é o que transforma um erro em conserto: sem ela, quem
// errou o nome não tem como descobrir o certo sem abrir o arquivo por SSH.
func TestUnknownRunnerListsAvailableOnes(t *testing.T) {
	catalog, err := Parse([]byte(`{"claude":{"cmd":["claude"]},"codex":{"cmd":["codex"]}}`))
	if err != nil {
		t.Fatalf("catálogo válido não devia falhar: %v", err)
	}
	_, _, err = catalog.Resolve("inventado", "/tmp/p.txt")
	if !errors.Is(err, ErrUnknownRunner) {
		t.Fatalf("esperava ErrUnknownRunner, veio %v", err)
	}
	for _, esperado := range []string{"claude", "codex"} {
		if !strings.Contains(err.Error(), esperado) {
			t.Errorf("a mensagem devia listar %q: %v", esperado, err)
		}
	}
}

// Runner cadastrado mas não instalado diz QUAL binário falta.
//
// É o caso dos runners que ficam no catálogo antes de a máquina os ter. A
// mensagem precisa nomear o binário, senão o diagnóstico vira adivinhação.
func TestUninstalledRunnerNamesTheMissingBinary(t *testing.T) {
	catalog, _ := Parse([]byte(`{"codex":{"cmd":["codex-que-nao-existe","exec"]}}`))
	_, _, err := catalog.Resolve("codex", "/tmp/p.txt")
	if !errors.Is(err, ErrRunnerNotInstalled) {
		t.Fatalf("esperava ErrRunnerNotInstalled, veio %v", err)
	}
	if !strings.Contains(err.Error(), "codex-que-nao-existe") {
		t.Errorf("a mensagem devia nomear o binário: %v", err)
	}
}

// O marcador do prompt é trocado, e só ele.
func TestPromptPlaceholderIsReplacedPerArgument(t *testing.T) {
	// Um binário que existe em qualquer máquina, para o LookPath passar.
	catalog, err := Parse([]byte(`{"eco":{"cmd":["echo","-f","{prompt}","--fim"]}}`))
	if err != nil {
		t.Fatalf("catálogo: %v", err)
	}
	cmd, stdin, err := catalog.Resolve("eco", "/tmp/prompt-42.txt")
	if err != nil {
		t.Fatalf("resolução: %v", err)
	}
	if stdin {
		t.Error("este runner não usa entrada padrão")
	}
	esperado := []string{"echo", "-f", "/tmp/prompt-42.txt", "--fim"}
	if len(cmd) != len(esperado) {
		t.Fatalf("o vetor mudou de tamanho: %v", cmd)
	}
	for i := range esperado {
		if cmd[i] != esperado[i] {
			t.Errorf("posição %d: %q, esperava %q", i, cmd[i], esperado[i])
		}
	}
}

// Caminho de prompt com metacaractere NÃO vira comando.
//
// É a prova de que o vetor cumpre o que promete: no ralph, com `eval`, um
// caminho assim seria interpretado. Aqui ele continua sendo um argumento só.
func TestPromptPathWithMetacharactersStaysOneArgument(t *testing.T) {
	catalog, _ := Parse([]byte(`{"eco":{"cmd":["echo","{prompt}"]}}`))
	cmd, _, err := catalog.Resolve("eco", "/tmp/a; rm -rf /; echo $(whoami).txt")
	if err != nil {
		t.Fatalf("resolução: %v", err)
	}
	if len(cmd) != 2 {
		t.Fatalf("o caminho devia continuar sendo UM argumento, veio %v", cmd)
	}
	if !strings.Contains(cmd[1], "rm -rf") {
		t.Errorf("o texto devia ser preservado literalmente: %q", cmd[1])
	}
}

// A flag de entrada padrão é respeitada.
func TestStdinFlagIsCarried(t *testing.T) {
	catalog, _ := Parse([]byte(`{"eco":{"cmd":["echo","-"],"stdin":true}}`))
	_, stdin, err := catalog.Resolve("eco", "/tmp/p.txt")
	if err != nil {
		t.Fatalf("resolução: %v", err)
	}
	if !stdin {
		t.Error("stdin devia vir marcado")
	}
}

// Catálogo vazio recusa com mensagem própria.
func TestEmptyCatalogHasItsOwnMessage(t *testing.T) {
	catalog, _ := Parse([]byte(`{}`))
	_, _, err := catalog.Resolve("claude", "/tmp/p.txt")
	if !errors.Is(err, ErrUnknownRunner) {
		t.Fatalf("esperava ErrUnknownRunner, veio %v", err)
	}
	if !strings.Contains(err.Error(), "nenhum runner cadastrado") {
		t.Errorf("a mensagem devia distinguir catálogo vazio: %v", err)
	}
}

// Um catálogo real, lido do disco, funciona ponta a ponta.
func TestRealCatalogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runners.json")
	conteudo := `{
  "claude": {"cmd": ["echo", "-p", "{prompt}"], "description": "Claude Code"},
  "codex":  {"cmd": ["codex", "exec", "--yolo", "-"], "stdin": true, "description": "Codex"}
}`
	if err := os.WriteFile(path, []byte(conteudo), 0o640); err != nil {
		t.Fatalf("gravando: %v", err)
	}
	catalog, err := Load(path)
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}
	nomes := catalog.Names()
	if len(nomes) != 2 || nomes[0] != "claude" || nomes[1] != "codex" {
		t.Fatalf("nomes fora de ordem ou incompletos: %v", nomes)
	}
}
