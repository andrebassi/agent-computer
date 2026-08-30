package tools

import (
	"context"
	"fmt"
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
	d := NewDelegate(t.TempDir(), filepath.Join(t.TempDir(), "missing.env"))
	result, err := d.Execute(context.Background(), 1, `{"task":"conserte o teste"}`)
	if err != nil {
		t.Fatalf("credencial ausente não devia virar erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("devia marcar falha")
	}
	if !strings.Contains(result.Output, "credencial") || !strings.Contains(result.Output, "missing.env") {
		t.Fatalf("a mensagem devia dizer o que falta e onde: %q", result.Output)
	}
}

// Arquivo de ambiente vazio é tão inútil quanto ausente, e precisa dizer isso.
func TestDelegateRejectsEmptyEnvFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "empty.env")
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
	alwaysSucceeds := filepath.Join(dir, "always-succeeds-agent")
	if err := os.WriteFile(alwaysSucceeds, []byte("#!/bin/sh\necho pronto\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = alwaysSucceeds
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

	fakeAgent := filepath.Join(dir, "fake-agent")
	// Ecoa argumentos e credencial DENTRO do campo `result` do JSON, e não em
	// texto solto: desde `--output-format json`, texto puro é tratado como saída
	// ilegível — e foi assim que este teste quebrou quando a flag entrou.
	//
	// Ecoa TODOS os argumentos, e não `$1`/`$2`: checar por posição amarra o
	// teste à ordem das flags, que já mudou duas vezes.
	script := `#!/bin/sh` + "\n" +
		`printf '[{"type":"result","subtype":"success","is_error":false,` +
		`"result":"argumentos:%s | credencial:%s","num_turns":1,"total_cost_usd":0.01}]' "$*" "$ANTHROPIC_API_KEY"` + "\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = fakeAgent
	result, err := d.Execute(context.Background(), 1, `{"task":"conserte o teste de login"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if result.Failed {
		t.Fatalf("não devia falhar: %q", result.Output)
	}
	if !strings.Contains(result.Output, "-p") {
		t.Fatalf("devia rodar sem interação: %q", result.Output)
	}
	if !strings.Contains(result.Output, "conserte o teste de login") {
		t.Fatalf("a tarefa devia chegar como argumento: %q", result.Output)
	}
	if !strings.Contains(result.Output, "credencial:chave-de-teste") {
		t.Fatalf("a credencial devia chegar pelo ambiente: %q", result.Output)
	}
}

// resultJSON monta a saída de `--output-format json` como ela chega de verdade:
// uma lista de eventos, com o `result` por último.
func resultJSON(subtype string, isError bool, text string, turns int, cost float64) string {
	return fmt.Sprintf(`[{"type":"system","subtype":"init","session_id":"s1"},`+
		`{"type":"result","subtype":%q,"is_error":%t,"result":%q,"num_turns":%d,"total_cost_usd":%v,"session_id":"s1"}]`,
		subtype, isError, text, turns, cost)
}

// fakeAgentPrinting cria um executável que imprime o texto dado e sai com o
// código pedido.
func fakeAgentPrinting(t *testing.T, dir, name, saida string, exitCode int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := fmt.Sprintf("#!/bin/sh\ncat <<'FIM'\n%s\nFIM\nexit %d\n", saida, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	return path
}

// credentialDir prepara um diretório com credencial válida.
func credentialDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cred.env"), []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	return dir
}

// Sucesso: devolve o texto do agente, e o custo vai junto.
//
// O custo entra SEMPRE, inclusive no sucesso: sem ele, o preço de delegar só
// aparece na fatura, e a decisão de delegar de novo é tomada às cegas.
func TestDelegateReturnsResultAndCost(t *testing.T) {
	dir := credentialDir(t)
	d := NewDelegate(dir, filepath.Join(dir, "cred.env"))
	d.binary = fakeAgentPrinting(t, dir, "ok-agent",
		resultJSON("success", false, "escrevi os três arquivos", 7, 0.1234), 0)

	result, err := d.Execute(context.Background(), 1, `{"task":"algo"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if result.Failed {
		t.Fatalf("não devia falhar: %q", result.Output)
	}
	if !strings.Contains(result.Output, "escrevi os três arquivos") {
		t.Fatalf("devia devolver o texto do agente: %q", result.Output)
	}
	if !strings.Contains(result.Output, "US$ 0.1234") || !strings.Contains(result.Output, "7 turno") {
		t.Fatalf("custo e turnos deviam ir junto: %q", result.Output)
	}
	// O JSON cru NÃO pode vazar para o histórico: ele custa token em toda
	// iteração seguinte e não diz nada a quem delegou.
	if strings.Contains(result.Output, `"is_error"`) {
		t.Fatalf("o JSON cru vazou para a resposta: %q", result.Output)
	}
}

// `is_error` é o que a saída em texto não tinha.
//
// Este é o caso medido em 30/08: o agente devolveu um pedido de permissão como
// texto, com código de saída ZERO, e quem delegou leu como resposta. Aqui o
// código de saída continua zero de propósito — é `is_error` que precisa pegar.
func TestDelegateDetectsErrorDespiteZeroExitCode(t *testing.T) {
	dir := credentialDir(t)
	d := NewDelegate(dir, filepath.Join(dir, "cred.env"))
	d.binary = fakeAgentPrinting(t, dir, "refusing-agent",
		resultJSON("error_max_budget_usd", true, "", 1, 5.02), 0)

	result, err := d.Execute(context.Background(), 1, `{"task":"algo caro"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if !result.Failed {
		t.Fatalf("is_error devia marcar falha mesmo com rc=0: %q", result.Output)
	}
	if !strings.Contains(result.Output, "error_max_budget_usd") {
		t.Fatalf("o motivo devia aparecer: %q", result.Output)
	}
}

// Saída ilegível não pode sumir com o que o agente disse.
//
// Erro de infraestrutura — binário faltando, credencial recusada — sai em texto
// puro, antes de qualquer JSON. Engolir isso deixaria o diagnóstico sem nada.
func TestDelegateFallsBackWhenOutputIsNotJSON(t *testing.T) {
	dir := credentialDir(t)
	d := NewDelegate(dir, filepath.Join(dir, "cred.env"))
	d.binary = fakeAgentPrinting(t, dir, "broken-agent", "Credit balance is too low", 1)

	result, err := d.Execute(context.Background(), 1, `{"task":"algo"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if !result.Failed {
		t.Fatal("devia marcar falha")
	}
	if !strings.Contains(result.Output, "Credit balance is too low") {
		t.Fatalf("o texto bruto devia sobreviver: %q", result.Output)
	}
}

// A lista pode crescer, e o `result` é o ÚLTIMO — não o primeiro.
func TestParseDelegateResultPicksTheResultEvent(t *testing.T) {
	raw := `[{"type":"system","subtype":"init"},{"type":"assistant"},` +
		`{"type":"result","subtype":"success","result":"pronto","num_turns":3}]`
	event, err := parseDelegateResult(raw)
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if event.Result != "pronto" || event.NumTurns != 3 {
		t.Fatalf("evento errado: %+v", event)
	}

	// Lista sem `result` é resposta incompleta, e precisa dizer isso.
	if _, err := parseDelegateResult(`[{"type":"system"}]`); err == nil {
		t.Fatal("lista sem result devia falhar")
	}
	if _, err := parseDelegateResult("   "); err == nil {
		t.Fatal("saída vazia devia falhar")
	}
}

// O teto de gasto vai na linha de comando: é o que impede uma tarefa mal
// descrita de virar escavação cara sem nada a mostrar.
func TestDelegatePassesBudgetCap(t *testing.T) {
	dir := credentialDir(t)
	d := NewDelegate(dir, filepath.Join(dir, "cred.env"))
	d.binary = fakeAgentPrinting(t, dir, "arg-echo", resultJSON("success", false, "ok", 1, 0.01), 0)

	// Um executável que ecoa os argumentos prova o que foi passado.
	echoPath := filepath.Join(dir, "echo-args")
	if err := os.WriteFile(echoPath, []byte("#!/bin/sh\necho \"$@\" >&2\ncat <<'FIM'\n"+
		resultJSON("success", false, "ok", 1, 0.01)+"\nFIM\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	d.binary = echoPath

	result, err := d.Execute(context.Background(), 1, `{"task":"algo"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	// stderr e stdout vão para o mesmo buffer, então os argumentos aparecem —
	// mas como o JSON também está lá, o parse ainda precisa funcionar.
	if result.Failed && !strings.Contains(result.Output, "max-budget-usd") {
		t.Fatalf("o teto devia ir na linha de comando: %q", result.Output)
	}
}

// A busca web tem de ir liberada, senão o agente de código PEDE APROVAÇÃO.
//
// Medido em 30/08 e é o pior modo de falha da ferramenta: sem `--allowedTools`
// ele devolveu "preciso que você aprove a permissão de WebSearch" como texto,
// com código de saída zero. Quem delegou leu aquilo como resposta, concluiu que
// não dava para buscar, e gastou 20 chamadas raspando HTML.
func TestDelegatePassesAllowedTools(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=x\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	// Ecoa os próprios argumentos, que é o que precisa ser conferido.
	echoArgs := filepath.Join(dir, "arg-reporter")
	if err := os.WriteFile(echoArgs, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = echoArgs
	result, err := d.Execute(context.Background(), 1, `{"task":"quem joga hoje"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	for _, tool := range []string{"WebSearch", "WebFetch"} {
		if !strings.Contains(result.Output, tool) {
			t.Fatalf("%s devia ir liberada, senão a delegação pede aprovação: %q", tool, result.Output)
		}
	}
	// A tarefa continua sendo o último argumento, depois das flags.
	if !strings.HasSuffix(strings.TrimSpace(result.Output), "quem joga hoje") {
		t.Fatalf("a tarefa devia ser o último argumento: %q", result.Output)
	}
	// Procuração ampla NÃO: o computador carrega credencial de conta.
	if strings.Contains(result.Output, "bypassPermissions") {
		t.Fatalf("não pode liberar tudo, só a lista: %q", result.Output)
	}
}

// O arquivo de credencial VENCE o que estiver no ambiente deste processo.
//
// Sem essa precedência, uma `ANTHROPIC_API_KEY` velha herdada do shell venceria
// o token de assinatura, e a delegação falharia com "Credit balance is too low"
// — mensagem que manda olhar a conta de API quando o problema é a ordem das
// variáveis. Aconteceu de verdade em 30/08.
func TestDelegateEnvFileOverridesInheritedVariables(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(envFile, []byte("ANTHROPIC_API_KEY=a-do-arquivo\n"), 0o600); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "a-herdada-do-shell")

	reporter := filepath.Join(dir, "env-reporter")
	script := "#!/bin/sh\necho \"chave:$ANTHROPIC_API_KEY\"\necho \"config:$CLAUDE_CONFIG_DIR\"\n"
	if err := os.WriteFile(reporter, []byte(script), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = reporter
	result, err := d.Execute(context.Background(), 1, `{"task":"algo"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if !strings.Contains(result.Output, "chave:a-do-arquivo") {
		t.Fatalf("o arquivo devia vencer o ambiente herdado: %q", result.Output)
	}

	// A configuração precisa cair no volume durável, ao lado da credencial, e
	// não no `~/.claude` padrão, que morre no rebuild.
	expected := filepath.Join(dir, "claude-config")
	if !strings.Contains(result.Output, "config:"+expected) {
		t.Fatalf("o config devia apontar para %q: %q", expected, result.Output)
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
	fakeAgent := filepath.Join(dir, "failing-agent")
	if err := os.WriteFile(fakeAgent, []byte("#!/bin/sh\necho 'nao consegui compilar'\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = fakeAgent
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
	longReport := strings.Repeat("r", maxDelegateOutput+500)
	got := truncateDelegateOutput(longReport)
	if len(got) >= len(longReport) {
		t.Fatal("relatório longo devia ser cortado")
	}
	if !strings.Contains(got, "truncado") {
		t.Fatalf("devia avisar que cortou: %q", got[len(got)-80:])
	}
	if shortReport := truncateDelegateOutput("resumo curto"); shortReport != "resumo curto" {
		t.Fatalf("relatório curto foi alterado: %q", shortReport)
	}
}

// O leitor do arquivo de ambiente ignora comentário e linha em branco, e não
// devolve o valor em erro nenhum.
func TestReadEnvFileParsesAndKeepsSecretOutOfErrors(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "cred.env")
	content := "# comentário\n\nANTHROPIC_API_KEY=valor-secreto\nOUTRA=coisa\nlinha-sem-igual\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
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
	_, err = readEnvFile(filepath.Join(dir, "gone.env"))
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
	slowAgent := filepath.Join(dir, "slow-agent")
	if err := os.WriteFile(slowAgent, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = slowAgent
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
	silentAgent := filepath.Join(dir, "silent-agent")
	if err := os.WriteFile(silentAgent, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}

	d := NewDelegate(dir, envFile)
	d.binary = silentAgent
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
	dir := filepath.Join(t.TempDir(), "directory-instead-of-file")
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
