package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// decodeArgs lê os argumentos de uma ferramenta RECUSANDO campo desconhecido.
//
// # Por que recusar em vez de ignorar
//
// `json.Unmarshal` descarta em silêncio o que não conhece. Num argumento vindo
// do MODELO isso é o pior comportamento possível: um `{"comand":"ls"}` — com o
// erro de digitação que um modelo comete — decodifica sem erro, deixa o campo
// certo vazio, e a ferramenta responde "comando vazio". A mensagem manda
// investigar a coisa errada, e o modelo tende a repetir a chamada em vez de
// olhar o nome do campo.
//
// É o mesmo raciocínio que já vale na porta HTTP: o servidor usa
// `DisallowUnknownFields` justamente porque campo ignorado esconde erro. As
// ferramentas ficaram de fora daquela decisão sem motivo — e são a superfície
// que recebe entrada de quem mais erra nome de campo.
//
// # Por que o erro é bom para o modelo
//
// A recusa volta como texto de ferramenta, não como falha da tarefa: o modelo
// lê "campo desconhecido", corrige o nome e segue. Executar com o campo faltando
// gastaria o mesmo turno e produziria um resultado errado que ninguém questiona.
func decodeArgs(arguments string, target any) error {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		// Argumento vazio é legítimo para ferramenta sem parâmetro obrigatório.
		// Deixar o alvo zerado é o que o `Unmarshal` faria, e recusar aqui
		// quebraria chamada válida.
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("argumentos inválidos: %w", err)
	}
	return nil
}
