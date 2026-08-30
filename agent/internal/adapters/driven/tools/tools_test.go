package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// A ferramenta de take-over precisa aceitar os cinco motivos da documentação e
// transformá-los em BlockRequest, que é o que faz o laço parar.
func TestTakeoverAcceptsDocumentedReasons(t *testing.T) {
	tool := NewTakeover()
	reasons := []string{"password", "two_factor", "captcha", "payment_identity", "human_required"}
	for _, r := range reasons {
		t.Run(r, func(t *testing.T) {
			args := `{"reason":"` + r + `","detail":"faça isso"}`
			res, err := tool.Execute(context.Background(), 1, args)
			if err != nil {
				t.Fatalf("Execute falhou: %v", err)
			}
			if res.BlockRequest == nil {
				t.Fatal("devia produzir um pedido de bloqueio")
			}
			if res.BlockRequest.Reason != domain.BlockReason(r) {
				t.Fatalf("motivo errado: %s", res.BlockRequest.Reason)
			}
		})
	}
}

// Motivo inventado não pode virar bloqueio: devolver a lista ao modelo o faz
// corrigir na iteração seguinte.
func TestTakeoverRejectsUnknownReason(t *testing.T) {
	res, err := NewTakeover().Execute(context.Background(), 1, `{"reason":"xyz","detail":"d"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if res.BlockRequest != nil {
		t.Fatal("motivo inválido não podia gerar bloqueio")
	}
	if !res.Failed {
		t.Fatal("resultado devia estar marcado como falho")
	}
	if !strings.Contains(res.Output, "password") {
		t.Fatalf("a saída devia listar os motivos válidos: %q", res.Output)
	}
}

// JSON malformado vem do modelo com alguma frequência; virar erro de execução
// derrubaria a tarefa sem necessidade.
func TestTakeoverHandlesMalformedArguments(t *testing.T) {
	res, err := NewTakeover().Execute(context.Background(), 1, `{isso não é json`)
	if err != nil {
		t.Fatalf("argumento inválido não devia virar erro de execução: %v", err)
	}
	if !res.Failed || res.BlockRequest != nil {
		t.Fatalf("devia falhar sem bloquear: %+v", res)
	}
}

// A descrição da ferramenta é o que ensina o modelo a parar em vez de tentar
// contornar. Se ela perder a ênfase, o comportamento muda.
func TestTakeoverSpecTellsTheModelNotToBypass(t *testing.T) {
	spec := NewTakeover().Spec()
	if spec.Name != "request_takeover" {
		t.Fatalf("nome inesperado: %s", spec.Name)
	}
	for _, termo := range []string{"CAPTCHA", "senha", "contornar"} {
		if !strings.Contains(spec.Description, termo) {
			t.Fatalf("a descrição devia mencionar %q: %q", termo, spec.Description)
		}
	}
}

// O caminho normal do shell: comando roda e a saída volta.
func TestShellRunsCommand(t *testing.T) {
	res, err := NewShell(t.TempDir()).Execute(context.Background(), 1, `{"command":"echo ola"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if !strings.Contains(res.Output, "ola") {
		t.Fatalf("saída inesperada: %q", res.Output)
	}
	if res.Failed {
		t.Fatal("comando bem-sucedido não devia ser marcado como falho")
	}
}

// Código de saída diferente de zero é informação, não erro de ferramenta:
// `grep` sem resultado já devolve 1.
func TestShellReportsNonZeroExitWithoutError(t *testing.T) {
	res, err := NewShell(t.TempDir()).Execute(context.Background(), 1, `{"command":"exit 3"}`)
	if err != nil {
		t.Fatalf("saída não-zero não devia virar erro de execução: %v", err)
	}
	if !res.Failed {
		t.Fatal("resultado devia indicar falha do comando")
	}
}

// Comando vazio e argumentos malformados precisam de mensagem clara.
func TestShellRejectsEmptyAndMalformedInput(t *testing.T) {
	shell := NewShell(t.TempDir())
	cases := map[string]string{
		"json inválido": `{quebrado`,
		"comando vazio": `{"command":"   "}`,
	}
	for nome, args := range cases {
		t.Run(nome, func(t *testing.T) {
			res, err := shell.Execute(context.Background(), 1, args)
			if err != nil {
				t.Fatalf("não devia virar erro de execução: %v", err)
			}
			if !res.Failed {
				t.Fatalf("devia falhar: %+v", res)
			}
		})
	}
}

// O diretório de trabalho padrão precisa valer, senão o agente grava fora do
// workspace durável.
func TestShellUsesConfiguredWorkdir(t *testing.T) {
	dir := t.TempDir()
	res, err := NewShell(dir).Execute(context.Background(), 1, `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	// No macOS /var é link para /private/var, então comparar o sufixo evita um
	// falso negativo por causa do caminho canônico.
	if !strings.Contains(res.Output, strings.TrimPrefix(dir, "/private")) {
		t.Fatalf("comando não rodou no diretório configurado: %q (esperava %s)", res.Output, dir)
	}
}

// O parâmetro workdir da chamada sobrepõe o padrão.
func TestShellHonoursPerCallWorkdir(t *testing.T) {
	outro := t.TempDir()
	res, err := NewShell(t.TempDir()).Execute(context.Background(), 1, `{"command":"pwd","workdir":"`+outro+`"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if !strings.Contains(res.Output, strings.TrimPrefix(outro, "/private")) {
		t.Fatalf("workdir da chamada foi ignorado: %q", res.Output)
	}
}

// Comando sem saída precisa devolver algo: string vazia no histórico faz o
// modelo achar que a ferramenta não rodou.
func TestShellReportsEmptyOutputExplicitly(t *testing.T) {
	res, err := NewShell(t.TempDir()).Execute(context.Background(), 1, `{"command":"true"}`)
	if err != nil {
		t.Fatalf("Execute falhou: %v", err)
	}
	if res.Output != "(sem saída)" {
		t.Fatalf("esperava marcador de saída vazia, veio %q", res.Output)
	}
}

// Saída longa é cortada pelo MEIO. Cortar só o fim perderia a mensagem de erro,
// que costuma estar na última linha.
func TestTruncateOutputKeepsHeadAndTail(t *testing.T) {
	texto := strings.Repeat("A", 100) + strings.Repeat("M", maxOutputBytes) + strings.Repeat("Z", 100)
	got := truncateOutput(texto)
	if len(got) >= len(texto) {
		t.Fatal("texto longo devia ser encurtado")
	}
	if !strings.HasPrefix(got, "AAAA") {
		t.Fatalf("o começo devia ser preservado: %q", got[:20])
	}
	if !strings.HasSuffix(got, "ZZZZ") {
		t.Fatalf("o fim devia ser preservado: %q", got[len(got)-20:])
	}
	if !strings.Contains(got, "omitidos do meio") {
		t.Fatal("devia indicar quanto foi omitido")
	}
}

// Saída curta passa intacta.
func TestTruncateOutputLeavesShortTextAlone(t *testing.T) {
	if got := truncateOutput("curto"); got != "curto" {
		t.Fatalf("texto curto foi alterado: %q", got)
	}
}

// A descrição do shell precisa ensinar a fronteira entre durável e descartável,
// senão o agente grava trabalho em /scratch e o perde no próximo update.
func TestShellSpecExplainsDurableBoundary(t *testing.T) {
	spec := NewShell("/workspace").Spec()
	if !strings.Contains(spec.Description, "/workspace") || !strings.Contains(spec.Description, "/scratch") {
		t.Fatalf("a descrição devia explicar a fronteira: %q", spec.Description)
	}
}
