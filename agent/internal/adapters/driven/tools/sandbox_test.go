package tools

import (
	"context"
	"strings"
	"testing"
)

// Sem usuário, o comando roda como está.
//
// É o caso da máquina de desenvolvimento e dos testes, onde não existem dois
// usuários — e onde embrulhar em sudo faria toda a suíte pedir senha.
func TestSandboxWithoutUserRunsCommandDirectly(t *testing.T) {
	cmd := NewSandbox("").Command(context.Background(), "echo", "oi")
	if strings.Contains(cmd.Path, "sudo") {
		t.Fatalf("não devia embrulhar em sudo: %s", cmd.Path)
	}
	if cmd.Args[0] != "echo" || cmd.Args[1] != "oi" {
		t.Fatalf("argumentos alterados: %v", cmd.Args)
	}
}

// Com usuário, o comando cai para ele por sudo, sem prompt e com `--`.
//
// `-n` é o que impede a ferramenta de pendurar até o tempo limite esperando uma
// senha que ninguém vai digitar; `--` é o que impede um comando do modelo
// começando com hífen de virar opção do próprio sudo.
func TestSandboxDropsToUserWithoutPromptAndTerminatesOptions(t *testing.T) {
	cmd := NewSandbox("agent").Command(context.Background(), "bash", "-c", "echo oi")
	joined := strings.Join(cmd.Args, " ")
	for _, expected := range []string{"sudo", "-n", "-u agent", "-- bash"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("faltou %q em: %s", expected, joined)
		}
	}
	// O comando do modelo precisa chegar depois do `--`, intacto.
	separator := indexOf(cmd.Args, "--")
	if separator < 0 || cmd.Args[separator+1] != "bash" {
		t.Fatalf("o comando não veio logo após o separador: %v", cmd.Args)
	}
}

// Um comando do modelo que começa com hífen NÃO vira opção do sudo.
//
// Canário do `--`: sem ele, um comando como "-h" seria consumido pelo sudo e o
// modelo receberia a ajuda do sudo como se fosse a saída do seu comando.
func TestSandboxProtectsCommandsStartingWithDash(t *testing.T) {
	cmd := NewSandbox("agent").Command(context.Background(), "-h")
	separator := indexOf(cmd.Args, "--")
	if separator < 0 {
		t.Fatalf("sem separador: %v", cmd.Args)
	}
	if cmd.Args[separator+1] != "-h" {
		t.Fatalf("o comando devia vir depois do separador: %v", cmd.Args)
	}
}

// Só as variáveis nomeadas atravessam o sudo.
//
// O padrão do sudo é limpar o ambiente, e é isso que se quer: cada variável que
// passa é uma decisão declarada, não um resto herdado. As nomeadas viajam por
// AMBIENTE, nunca por argumento — `ps` mostra a linha de comando de qualquer
// processo a qualquer usuário da máquina.
func TestSandboxPreservesOnlyNamedVariables(t *testing.T) {
	cmd := NewSandbox("agent", "CLAUDE_CODE_OAUTH_TOKEN", "HOME").
		Command(context.Background(), "bash", "-c", "true")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--preserve-env=CLAUDE_CODE_OAUTH_TOKEN,HOME") {
		t.Fatalf("a lista de variáveis preservadas não apareceu: %s", joined)
	}
}

// Nenhum valor de segredo pode aparecer na linha de comando montada.
//
// Teste genérico de propósito: varre os argumentos inteiros em vez de conferir
// posição a posição, para pegar também o argumento que alguém acrescentar
// depois. Argumento é público para a máquina toda.
func TestSandboxNeverPutsSecretValuesInArguments(t *testing.T) {
	const secretValue = "token-secreto-que-nao-pode-ir-no-argv"
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", secretValue)
	cmd := NewSandbox("agent", "CLAUDE_CODE_OAUTH_TOKEN").
		Command(context.Background(), "bash", "-c", "true")
	for _, arg := range cmd.Args {
		if strings.Contains(arg, secretValue) {
			t.Fatalf("o segredo foi para a linha de comando: %q", arg)
		}
	}
}

// Describe denuncia a máquina que subiu SEM rebaixamento.
//
// Sem essa linha no log, uma máquina desprotegida parece idêntica a uma
// protegida até alguém ler o cofre pelo shell do modelo — e aí é tarde.
func TestDescribeTellsWhetherDowngradeIsOn(t *testing.T) {
	off := NewSandbox("").Describe()
	if !strings.Contains(off, "sem rebaixamento") {
		t.Fatalf("devia denunciar a ausência: %q", off)
	}
	on := NewSandbox("agent").Describe()
	if !strings.Contains(on, "agent") {
		t.Fatalf("devia nomear o usuário: %q", on)
	}
}

// Rebaixar para o PRÓPRIO usuário não embrulha em sudo.
//
// Acontece de verdade: os scripts de operação rodam o agentd por SSH como
// `agent`, o mesmo usuário de destino. Ali um `sudo -u agent` exigiria uma
// concessão de sudoers que não existe, e o comando falharia com "not allowed to
// execute" — mensagem que manda procurar no lugar errado.
func TestSandboxSkipsWhenAlreadyTheTargetUser(t *testing.T) {
	me := currentUserName()
	if me == "" {
		t.Skip("não consegui descobrir o usuário do processo")
	}
	cmd := NewSandbox(me).Command(context.Background(), "echo", "oi")
	if strings.Contains(cmd.Path, "sudo") {
		t.Fatalf("não devia embrulhar em sudo para o próprio usuário: %v", cmd.Args)
	}
}

// "off" desliga o rebaixamento de forma explícita.
//
// Precisa ser uma palavra, e não string vazia: vazia é indistinguível de
// "esqueceram de definir", e nesse caso o padrão tem de ser o seguro.
func TestSandboxDisableRequiresAnExplicitWord(t *testing.T) {
	if NewSandbox(disableToken).Enabled() {
		t.Fatal("o desligamento explícito não funcionou")
	}
	// E o inverso: um usuário de destino real continua ligado.
	if !NewSandbox("um-usuario-que-nao-existe-nesta-maquina").Enabled() {
		t.Fatal("usuário de destino real devia manter o rebaixamento ligado")
	}
}

// A DELEGAÇÃO também é rebaixada, não só o shell.
//
// Foi um descuido real: o sandbox entrou na ferramenta de shell e ficou de fora
// da delegação, então o agente de código rodava como o usuário do serviço — o
// dono da identidade do cofre. Como ele executa comando arbitrário por desenho,
// uma delegação bastava para ler o arquivo de senha.
//
// Este teste é o que impede a regressão: sem o sandbox no construtor, o comando
// montado deixa de passar por sudo e ele reprova.
func TestDelegateIsAlsoDowngraded(t *testing.T) {
	tool := NewDelegateSandboxed("/workspace", "/workspace/agent/anthropic.env",
		NewSandbox("outro-usuario"))
	cmd := tool.sandbox.Command(context.Background(), "claude", "-p", "tarefa")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "sudo") || !strings.Contains(joined, "-u outro-usuario") {
		t.Fatalf("a delegação devia ser rebaixada: %s", joined)
	}
}

// O construtor simples NÃO rebaixa — e é o de teste, não o de produção.
//
// A distinção precisa ser explícita: um construtor que silenciosamente não
// protege, com nome que não diz isso, é como o descuido acima acontece.
func TestPlainDelegateConstructorDoesNotDowngrade(t *testing.T) {
	tool := NewDelegate("/workspace", "/workspace/agent/anthropic.env")
	if tool.sandbox.Enabled() {
		t.Fatal("o construtor simples não devia rebaixar")
	}
}

// indexOf devolve a posição do primeiro item igual ao procurado, ou -1.
func indexOf(items []string, wanted string) int {
	for i, item := range items {
		if item == wanted {
			return i
		}
	}
	return -1
}
