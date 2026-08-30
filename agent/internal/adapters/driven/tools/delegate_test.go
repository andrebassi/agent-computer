package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A descrição precisa dizer quando NÃO delegar.
//
// Sem esse limite, o modelo delega tudo — inclusive o que faria melhor sozinho —
// e a tarefa paga duas inferências para chegar ao mesmo lugar.
func TestDelegateSpecSaysWhenNotToDelegate(t *testing.T) {
	spec := NewDelegate("/workspace", "/tmp/env").Spec()
	if spec.Name != "delegate_to_code" {
		t.Fatalf("nome inesperado: %s", spec.Name)
	}
	if !strings.Contains(spec.Description, "NÃO use") {
		t.Fatalf("a descrição devia dizer quando não delegar: %q", spec.Description)
	}
	// O outro agente não vê a conversa; sem esse aviso o modelo delega pedidos
	// que só fazem sentido com o contexto anterior.
	if !strings.Contains(spec.Description, "não vê esta conversa") {
		t.Fatalf("a descrição devia avisar que o destino não tem contexto: %q", spec.Description)
	}
}

// Sem credencial configurada, a mensagem precisa dizer O QUE falta e ONDE.
//
// É o erro mais provável em máquina recém-criada, e um "falhou" genérico faria
// procurar defeito no agente de código em vez de no arquivo de ambiente.
func TestDelegateReportsMissingCredential(t *testing.T) {
	d := NewDelegate(t.TempDir(), filepath.Join(t.TempDir(), "nao-existe.env"))
	result, err := d.Execute(context.Background(), 1, `{"task":"conserte o teste"}`)
	if err != nil {
		t.Fatalf("credencial ausente não devia virar erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("devia marcar falha")
	}
	if !strings.Contains(result.Output, "credencial") || !strings.Contains(result.Output, "nao-existe.env") {
		t.Fatalf("a mensagem devia dizer o que falta e onde: %q", result.Output)
	}
}

// Arquivo de ambiente vazio é tão inútil quanto ausente, e precisa dizer isso.
func TestDelegateRejectsEmptyEnvFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "vazio.env")
	if err := os.WriteFile(envFile, []byte("# só um comentário\n\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	result, err := NewDelegate(dir, envFile).Execute(context.Background(), 1, `{"task":"algo"}`)
	if err != nil {
		t.Fatalf("não devia virar erro de execução: %v", err)
	}
	if !result.Failed || !strings.Contains(result.Output, "variável") {
		t.Fatalf("devia reclamar de arquivo sem variável: %q", result.Output)
	}
}

// Tarefa vazia é barrada antes de gastar uma execução inteira.
//
// A credencial existe de propósito: com ela ausente, a falha viria do arquivo
// que falta e o teste passaria pelo motivo errado — foi assim que ele nasceu
// decorativo, e um canário que removeu a validação o pegou verde.
func TestDelegateRejectsEmptyTask(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	// Um executável que sempre dá certo: se a validação sumir, a chamada passa
	// e o teste reprova — que é o que o canário precisa enxergar.
	sempreOk := filepath.Join(dir, "agente-que-aceita-tudo")
	if err := os.WriteFile(sempreOk, []byte("#!/bin/sh\necho pronto\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = sempreOk
	for _, args := range []string{`{}`, `{"task":"   "}`} {
		result, err := d.Execute(context.Background(), 1, args)
		if err != nil {
			t.Fatalf("não devia virar erro de execução: %v", err)
		}
		if !result.Failed {
			t.Fatalf("tarefa vazia devia falhar: %q", args)
		}
		if !strings.Contains(result.Output, "descreva a tarefa") {
			t.Fatalf("devia falhar POR tarefa vazia, veio %q", result.Output)
		}
	}
}

// Argumento malformado vem do modelo com alguma frequência, e a mensagem tem de
// dizer que o problema é o argumento — não a credencial, que aqui existe.
func TestDelegateHandlesMalformedArguments(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	result, err := NewDelegate(dir, envFile).Execute(context.Background(), 1, `{quebrado`)
	if err != nil {
		t.Fatalf("não devia virar erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("argumento inválido devia falhar")
	}
	if !strings.Contains(result.Output, "argumentos inválidos") {
		t.Fatalf("devia falhar POR argumento inválido, veio %q", result.Output)
	}
}

// O caminho feliz, com um executável falso no lugar do agente de código.
//
// O falso confere duas coisas que o teste real não veria: que a tarefa chega
// como argumento de `-p`, e que a credencial chega pelo ambiente — e não em
// linha de comando, onde `ps` a exporia.
func TestDelegateRunsAgentWithTaskAndCredential(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=chave-de-teste\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	falso := filepath.Join(dir, "agente-falso")
	script := "#!/bin/sh\necho \"modo:$1\"\necho \"tarefa:$2\"\necho \"credencial:$ANTHROPIC_API_KEY\"\n"
	if err := os.WriteFile(falso, []byte(script), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = falso
	result, err := d.Execute(context.Background(), 1, `{"task":"conserte o teste de login"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if result.Failed {
		t.Fatalf("não devia falhar: %q", result.Output)
	}
	if !strings.Contains(result.Output, "modo:-p") {
		t.Fatalf("devia rodar sem interação: %q", result.Output)
	}
	if !strings.Contains(result.Output, "tarefa:conserte o teste de login") {
		t.Fatalf("a tarefa devia chegar como argumento: %q", result.Output)
	}
	if !strings.Contains(result.Output, "credencial:chave-de-teste") {
		t.Fatalf("a credencial devia chegar pelo ambiente: %q", result.Output)
	}
}

// Falha do agente de código volta como texto, e não derruba a tarefa: quem
// delegou pode tentar outra abordagem.
func TestDelegateReportsAgentFailureAsText(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	falso := filepath.Join(dir, "agente-que-falha")
	if err := os.WriteFile(falso, []byte("#!/bin/sh\necho 'nao consegui compilar'\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = falso
	result, err := d.Execute(context.Background(), 1, `{"task":"algo"}`)
	if err != nil {
		t.Fatalf("falha do agente não devia virar erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("devia marcar falha")
	}
	if !strings.Contains(result.Output, "nao consegui compilar") {
		t.Fatalf("a saída do agente devia voltar para quem delegou: %q", result.Output)
	}
}

// Relatório longo é cortado, porque entra no prompt de todas as iterações
// seguintes de quem delegou.
func TestTruncateDelegateOutput(t *testing.T) {
	longo := strings.Repeat("r", maxDelegateOutput+500)
	got := truncateDelegateOutput(longo)
	if len(got) >= len(longo) {
		t.Fatal("relatório longo devia ser cortado")
	}
	if !strings.Contains(got, "truncado") {
		t.Fatalf("devia avisar que cortou: %q", got[len(got)-80:])
	}
	if curto := truncateDelegateOutput("resumo curto"); curto != "resumo curto" {
		t.Fatalf("relatório curto foi alterado: %q", curto)
	}
}

// O leitor do arquivo de ambiente ignora comentário e linha em branco, e não
// devolve o valor em erro nenhum.
func TestReadEnvFileParsesAndKeepsSecretOutOfErrors(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	conteudo := "# comentário\n\nANTHROPIC_API_KEY=valor-secreto\nOUTRA=coisa\nlinha-sem-igual\n"
	if err := os.WriteFile(envFile, []byte(conteudo), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	env, err := readEnvFile(envFile)
	if err != nil {
		t.Fatalf("readEnvFile falhou: %v", err)
	}
	if len(env) != 2 {
		t.Fatalf("esperava 2 variáveis, veio %d: %v", len(env), env)
	}

	// O erro de arquivo ausente não pode citar valor nenhum.
	_, err = readEnvFile(filepath.Join(dir, "sumido.env"))
	if err == nil {
		t.Fatal("arquivo ausente devia falhar")
	}
	if strings.Contains(err.Error(), "valor-secreto") {
		t.Fatalf("o erro não pode conter o valor: %v", err)
	}
}

// Estouro de tempo precisa ser reconhecível e AVISAR que a árvore pode estar
// pela metade — é o modo de falha mais perigoso da delegação, porque o disco
// fica num estado que ninguém escolheu.
//
// O prazo real são 15 minutos; aqui quem estoura é o contexto do chamador,
// exercitando o mesmo ramo sem esperar por eles.
func TestDelegateReportsTimeout(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	lento := filepath.Join(dir, "agente-lento")
	if err := os.WriteFile(lento, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = lento
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	result, err := d.Execute(ctx, 1, `{"task":"algo demorado"}`)
	if err != nil {
		t.Fatalf("estouro não devia virar erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("devia marcar falha")
	}
	if !strings.Contains(result.Output, "pela metade") {
		t.Fatalf("devia avisar sobre a árvore inconsistente: %q", result.Output)
	}
}

// Agente que termina calado precisa devolver algo: string vazia no histórico
// faria o modelo achar que a ferramenta não rodou e repetir a delegação.
func TestDelegateReportsSilentAgent(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	mudo := filepath.Join(dir, "agente-mudo")
	if err := os.WriteFile(mudo, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = mudo
	result, err := d.Execute(context.Background(), 1, `{"task":"algo"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Fatal("saída vazia devia virar texto explícito")
	}
}

// Sem arquivo configurado é diferente de arquivo ausente, e o diagnóstico muda:
// um é erro de montagem do binário, o outro é credencial que falta.
func TestReadEnvFileRejectsEmptyPath(t *testing.T) {
	if _, err := readEnvFile(""); err == nil {
		t.Fatal("caminho vazio devia falhar")
	}
}

// Erro de leitura que NÃO é "arquivo ausente" precisa propagar como está — um
// diretório no lugar do arquivo não é falta de credencial.
func TestReadEnvFileSurfacesOtherReadErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "diretorio-no-lugar-do-arquivo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	_, err := readEnvFile(dir)
	if err == nil {
		t.Fatal("diretório no lugar do arquivo devia falhar")
	}
	if strings.Contains(err.Error(), "falta a credencial") {
		t.Fatalf("não é credencial ausente, é erro de leitura: %v", err)
	}
}
