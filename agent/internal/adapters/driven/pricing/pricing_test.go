package pricing

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// grokTable reproduz a tabela real, para as contas serem conferíveis à mão.
const grokTable = `{
  "grok-4.6": {
    "small_prompt": {"input_per_1m": 2.00, "cached_per_1m": 0.50, "output_per_1m": 6.00},
    "large_prompt": {"input_per_1m": 4.00, "cached_per_1m": 1.00, "output_per_1m": 12.00},
    "source": "docs.x.ai/docs/models, consultado em 2026-08-31"
  }
}`

// closeEnough compara em dólares com tolerância de um centésimo de centavo.
func closeEnough(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// A conta da faixa pequena bate com o valor calculado à mão.
//
// 100.000 novos a 2,00/1M = 0,20 · 50.000 em cache a 0,50/1M = 0,025 ·
// 10.000 de saída a 6,00/1M = 0,06 → 0,285.
func TestSmallPromptCostMatchesHandCalculation(t *testing.T) {
	table, err := Parse([]byte(grokTable))
	if err != nil {
		t.Fatalf("tabela: %v", err)
	}
	cost, known := table.Cost("grok-4.6", 150_000, 50_000, 10_000)
	if !known {
		t.Fatal("o modelo devia ter preço")
	}
	if !closeEnough(cost, 0.285) {
		t.Fatalf("esperava 0,285, veio %.6f", cost)
	}
}

// Acima de 200 mil tokens de prompt, o preço DOBRA.
//
// É a sutileza que uma tabela de preço único erraria por 100%, justamente nas
// tarefas caras — as de histórico longo.
func TestLargePromptUsesTheDoubledTier(t *testing.T) {
	table, _ := Parse([]byte(grokTable))

	// 199.999 → faixa pequena. Tudo novo, sem cache, sem saída.
	pequeno, _ := table.Cost("grok-4.6", 199_999, 0, 0)
	// 200.000 → faixa grande, o mesmo volume custando o dobro.
	grande, _ := table.Cost("grok-4.6", 200_000, 0, 0)

	if !(grande > pequeno*1.9) {
		t.Fatalf("a faixa grande devia ~dobrar: pequeno %.6f, grande %.6f", pequeno, grande)
	}
}

// O token em CACHE é cobrado mais barato que o novo.
//
// Ignorar o cache superestimaria a conta em até quatro vezes neste modelo, e um
// teto que superestima quatro vezes para a tarefa cedo demais.
func TestCachedTokensAreCheaperThanFreshOnes(t *testing.T) {
	table, _ := Parse([]byte(grokTable))
	tudoNovo, _ := table.Cost("grok-4.6", 100_000, 0, 0)
	tudoEmCache, _ := table.Cost("grok-4.6", 100_000, 100_000, 0)

	if !(tudoEmCache < tudoNovo) {
		t.Fatalf("cache devia ser mais barato: novo %.6f, cache %.6f", tudoNovo, tudoEmCache)
	}
	// 2,00 contra 0,50 é exatamente 4×.
	if !closeEnough(tudoNovo/tudoEmCache, 4.0) {
		t.Errorf("a razão devia ser 4×, veio %.3f", tudoNovo/tudoEmCache)
	}
}

// `cached` está CONTIDO em `prompt`, não somado.
//
// Se fosse somado, um prompt inteiramente em cache custaria mais que o mesmo
// prompt sem cache — o oposto do que acontece.
func TestCachedIsContainedInPromptNotAddedToIt(t *testing.T) {
	table, _ := Parse([]byte(grokTable))
	// 100k de prompt, 40k deles em cache: 60k novos + 40k cacheados.
	cost, _ := table.Cost("grok-4.6", 100_000, 40_000, 0)
	esperado := 60_000*2.00/1e6 + 40_000*0.50/1e6
	if !closeEnough(cost, esperado) {
		t.Fatalf("esperava %.6f, veio %.6f", esperado, cost)
	}
}

// Número incoerente da API não produz custo negativo.
func TestIncoherentUsageDoesNotUndercharge(t *testing.T) {
	table, _ := Parse([]byte(grokTable))
	// Mais cache do que prompt: impossível, mas a API é de terceiro.
	cost, _ := table.Cost("grok-4.6", 1_000, 9_999, 0)
	if cost < 0 {
		t.Fatalf("custo não pode ser negativo: %.6f", cost)
	}
	// Todo o prompt tratado como cache é o máximo desconto legítimo.
	if !closeEnough(cost, 1_000*0.50/1e6) {
		t.Fatalf("esperava o prompt inteiro no preço de cache, veio %.6f", cost)
	}
}

// Modelo SEM preço devolve `false`, e zero não é "de graça".
//
// A distinção é o ponto: quem chama precisa saber que não há preço, senão um
// modelo novo passaria a rodar sem teto sem nada indicar.
func TestUnknownModelIsNotFree(t *testing.T) {
	table, _ := Parse([]byte(grokTable))
	cost, known := table.Cost("modelo-que-ninguem-cadastrou", 1_000_000, 0, 1_000_000)
	if known {
		t.Fatal("não devia haver preço para este modelo")
	}
	if cost != 0 {
		t.Fatalf("sem preço o custo é zero por convenção, veio %.6f", cost)
	}
	if table.Known("modelo-que-ninguem-cadastrou") {
		t.Error("Known devia dizer que não conhece")
	}
}

// Tabela ausente é vazia, sem erro.
func TestMissingTableIsEmptyNotAnError(t *testing.T) {
	table, err := Load(filepath.Join(t.TempDir(), "nao-existe.json"))
	if err != nil {
		t.Fatalf("arquivo ausente não devia dar erro: %v", err)
	}
	if len(table.Names()) != 0 {
		t.Fatalf("devia vir vazia: %v", table.Names())
	}
}

// Tabela malformada é recusada, com a causa.
//
// O caso do campo com nome errado é o mais importante: ele viraria preço ZERO,
// e preço zero desliga o teto em silêncio.
func TestMalformedTableIsRejected(t *testing.T) {
	casos := map[string]string{
		"json truncado": `{"grok": `,
		"campo com nome errado": `{"grok":{"small_prompt":{"input_per_1k":2.0},` +
			`"source":"x"}}`,
		"sem preço de entrada": `{"grok":{"small_prompt":{"output_per_1m":6.0},"source":"x"}}`,
		"sem preço de saída":   `{"grok":{"small_prompt":{"input_per_1m":2.0},"source":"x"}}`,
		"sem origem":           `{"grok":{"small_prompt":{"input_per_1m":2.0,"output_per_1m":6.0}}}`,
		"preço negativo":       `{"grok":{"small_prompt":{"input_per_1m":-1,"output_per_1m":6.0},"source":"x"}}`,
	}
	for nome, conteudo := range casos {
		if _, err := Parse([]byte(conteudo)); err == nil {
			t.Errorf("%s devia ser recusado", nome)
		}
	}
}

// A origem do preço é obrigatória e sobrevive à leitura.
//
// Número de preço sem procedência não se confere — e a tabela envelhece, então
// quem olhar daqui a seis meses precisa saber de quando ela é.
func TestSourceIsRequiredAndPreserved(t *testing.T) {
	table, err := Parse([]byte(grokTable))
	if err != nil {
		t.Fatalf("tabela: %v", err)
	}
	entry := table.models["grok-4.6"]
	if !strings.Contains(entry.Source, "2026") {
		t.Errorf("a origem devia trazer a data: %q", entry.Source)
	}
}

// Sem faixa grande declarada, a pequena vale para qualquer tamanho.
//
// É o caso de fornecedor com preço único — não pode virar custo zero acima do
// limiar.
func TestSingleTierAppliesToEverySize(t *testing.T) {
	table, err := Parse([]byte(`{"simples":{"small_prompt":{"input_per_1m":1.0,"output_per_1m":1.0},"source":"teste"}}`))
	if err != nil {
		t.Fatalf("tabela: %v", err)
	}
	cost, known := table.Cost("simples", 500_000, 0, 0)
	if !known {
		t.Fatal("devia conhecer o modelo")
	}
	if !closeEnough(cost, 0.5) {
		t.Fatalf("esperava 0,5 com preço único, veio %.6f", cost)
	}
}
