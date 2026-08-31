// Package pricing converte tokens em dólares.
//
// Mora num arquivo do volume, e não numa constante compilada, por um motivo que
// já se conhece: **preço envelhece**. Uma tabela dentro do binário só se corrige
// recompilando, e uma tabela desatualizada é pior que nenhuma — o teto passa a
// cortar no lugar errado e ninguém desconfia, porque o número parece medido.
//
// O arquivo carrega a data e a fonte de cada entrada, de propósito: quem for
// conferir daqui a seis meses precisa saber de quando é o número, sem procurar
// no histórico do git.
package pricing

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// largePromptThreshold é onde o preço da xAI dobra.
//
// Não é detalhe de tabela: acima de 200 mil tokens de prompt, entrada e saída
// custam o dobro. Um agente com histórico longo cruza essa linha sem avisar, e
// uma conta que ignore a faixa erra por 100% justamente nas tarefas caras.
const largePromptThreshold = 200_000

// Tier é o preço de uma faixa, em dólares por milhão de tokens.
type Tier struct {
	Input  float64 `json:"input_per_1m"`
	Cached float64 `json:"cached_per_1m"`
	Output float64 `json:"output_per_1m"`
}

// Model reúne as duas faixas de preço de um modelo.
type Model struct {
	// Small vale abaixo do limiar de prompt grande.
	Small Tier `json:"small_prompt"`
	// Large vale a partir do limiar. Ausente (zerada) faz a faixa pequena valer
	// para tudo — é o caso de fornecedor com preço único.
	Large Tier `json:"large_prompt"`
	// Source diz de onde o número veio e quando. Texto livre, e obrigatório na
	// prática: número de preço sem procedência não se confere.
	Source string `json:"source"`
}

// Table é o catálogo de preços, por nome de modelo.
type Table struct {
	models map[string]Model
}

// Load lê a tabela de um arquivo JSON.
//
// Arquivo ausente devolve tabela VAZIA sem erro. A consequência está em `Cost`:
// sem preço, não há teto em dólar — o agente roda como antes, e o registro diz
// que não havia preço. Derrubar o agente por falta de tabela de preço seria
// trocar um risco financeiro por uma parada certa.
func Load(path string) (*Table, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Table{models: map[string]Model{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lendo a tabela de preços: %w", err)
	}
	return Parse(content)
}

// Parse valida e monta a tabela.
func Parse(content []byte) (*Table, error) {
	raw := map[string]Model{}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	// Campo desconhecido recusa: um `input_per_1k` com nome errado viraria preço
	// ZERO, e preço zero desliga o teto em silêncio — a pior falha possível numa
	// tabela de custo.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("tabela de preços inválida: %w", err)
	}
	for name, model := range raw {
		if model.Small.Input <= 0 || model.Small.Output <= 0 {
			return nil, fmt.Errorf("modelo %q sem preço de entrada ou de saída", name)
		}
		if model.Source == "" {
			return nil, fmt.Errorf("modelo %q sem a origem do preço", name)
		}
	}
	return &Table{models: raw}, nil
}

// Known diz se há preço para o modelo.
func (t *Table) Known(model string) bool {
	_, ok := t.models[model]
	return ok
}

// Names lista os modelos com preço, para a mensagem de diagnóstico.
func (t *Table) Names() []string {
	out := make([]string, 0, len(t.models))
	for name := range t.models {
		out = append(out, name)
	}
	return out
}

// Cost calcula o custo de UM turno, em dólares.
//
// `cached` está contido em `prompt`, e não somado a ele: a parcela em cache é
// cobrada mais barato, e o resto no preço cheio. Somar contaria o mesmo token
// duas vezes.
//
// Modelo sem preço devolve `0, false`. Zero **não** é "de graça" — é "não sei",
// e quem chama precisa distinguir os dois, senão um modelo novo passaria a
// rodar sem teto sem que nada indicasse.
func (t *Table) Cost(model string, prompt, cached, completion int) (float64, bool) {
	entry, ok := t.models[model]
	if !ok {
		return 0, false
	}

	tier := entry.Small
	// A faixa é escolhida pelo tamanho do PROMPT, que é o que o fornecedor
	// mede — não pelo total de tokens do turno.
	if prompt >= largePromptThreshold && entry.Large.Input > 0 {
		tier = entry.Large
	}

	// Defesa contra número incoerente vindo da API: `cached` maior que `prompt`
	// produziria entrada negativa e um custo menor que o real.
	if cached > prompt {
		cached = prompt
	}
	if cached < 0 {
		cached = 0
	}
	fresh := prompt - cached

	const perMillion = 1_000_000.0
	total := float64(fresh)*tier.Input/perMillion +
		float64(cached)*tier.Cached/perMillion +
		float64(completion)*tier.Output/perMillion
	return total, true
}
