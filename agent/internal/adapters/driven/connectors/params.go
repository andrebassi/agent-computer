package connectors

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// declaredParams extrai os nomes de parâmetro que o esquema da operação anuncia.
//
// Devolve `nil` quando o esquema não declara `properties` — e a distinção
// importa: um esquema vazio significa "não sei o que é válido", e nesse caso a
// validação é PULADA em vez de recusar tudo. Manifesto antigo ou sem esquema
// continua funcionando como sempre funcionou.
func declaredParams(rawSchema json.RawMessage) map[string]bool {
	trimmed := strings.TrimSpace(string(rawSchema))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		// Esquema malformado não pode barrar a chamada: quem o escreveu foi o
		// operador, e o erro dele não deve virar recusa que o MODELO recebe e
		// não consegue consertar.
		return nil
	}
	if len(parsed.Properties) == 0 {
		return nil
	}
	names := make(map[string]bool, len(parsed.Properties))
	for name := range parsed.Properties {
		names[name] = true
	}
	return names
}

// checkParams recusa parâmetro que o esquema não declara.
//
// # O que isto fecha
//
// O conector decodifica os argumentos para um mapa, e o que sobra depois de
// preencher o caminho vira **query string**. Consequência: um parâmetro que o
// modelo inventou — ou digitou errado — não falha; ele é anexado à URL e a API
// remota o ignora. O resultado volta certo em forma e errado em conteúdo, e é
// o pior desfecho possível, porque nada indica que algo deu errado.
//
// O caso concreto: `{"stat":"opened"}` em vez de `state`. Hoje a listagem volta
// SEM filtro, com todos os itens, e o modelo conclui que o filtro não funciona
// na API — quando ele só escreveu o nome errado.
//
// É o mesmo motivo do `DisallowUnknownFields` na porta HTTP. A diferença é que
// aqui o esquema é dinâmico, então a comparação é contra o que o manifesto
// declarou.
//
// # Por que não validar tipo nem obrigatoriedade
//
// Tipo errado a API remota recusa, com mensagem própria e melhor que a nossa.
// Campo obrigatório ausente idem. O que a API NÃO tem como reclamar é do
// parâmetro que ela não conhece — ela o ignora. É exatamente essa lacuna que
// esta função cobre, e nada além dela.
func checkParams(declared map[string]bool, params map[string]any) error {
	if declared == nil || len(params) == 0 {
		return nil
	}
	unknown := make([]string, 0, len(params))
	for name := range params {
		if !declared[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	// Ordenado para a mensagem ser estável: erro que muda de texto a cada
	// execução atrapalha quem compara duas rodadas.
	sort.Strings(unknown)
	valid := make([]string, 0, len(declared))
	for name := range declared {
		valid = append(valid, name)
	}
	sort.Strings(valid)

	return fmt.Errorf("parâmetro não declarado: %s; os aceitos são: %s",
		strings.Join(unknown, ", "), strings.Join(valid, ", "))
}
