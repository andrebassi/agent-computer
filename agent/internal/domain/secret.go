package domain

import (
	"errors"
	"fmt"
	"strings"
)

// SecretRequest é o pedido de um valor sigiloso ao humano.
//
// A documentação é literal quanto ao contrato: "the value is masked and is not
// added to the conversation". Aqui isso não é recomendação — é estrutura: o
// tipo simplesmente NÃO TEM campo para guardar o valor. O que a pessoa digita
// vai da tela direto ao destino, e o histórico do agente registra apenas que um
// segredo foi fornecido.
//
// Um campo `Value string` neste struct, ainda que sempre limpo depois, criaria
// a chance de o valor ser serializado num log, num dump de estado ou no próximo
// turno enviado ao modelo. Não existir é a única garantia que não depende de
// alguém lembrar de limpá-lo.
type SecretRequest struct {
	ID string
	// Label descreve o que se pede, sem revelar nada: "senha do painel",
	// "código de 6 dígitos". É isto que aparece na tela e no histórico.
	Label string
	// Destination diz para onde o valor vai, para a pessoa poder conferir antes
	// de digitar. Sem isso, o agente poderia pedir uma senha e mandá-la para
	// outro lugar sem que ninguém percebesse.
	Destination string
	// Fulfilled marca que a pessoa respondeu. O valor não fica aqui.
	Fulfilled bool
}

// ErrEmptySecretLabel impede pedido sem descrição: um campo pedindo "digite o
// valor", sem dizer qual nem para onde vai, é exatamente a forma de um golpe.
var ErrEmptySecretLabel = errors.New("pedido de segredo sem descrição")

// ErrEmptySecretDestination impede pedido sem destino declarado.
var ErrEmptySecretDestination = errors.New("pedido de segredo sem destino declarado")

// NewSecretRequest monta o pedido, exigindo descrição e destino.
func NewSecretRequest(id, label, destination string) (*SecretRequest, error) {
	if strings.TrimSpace(label) == "" {
		return nil, ErrEmptySecretLabel
	}
	if strings.TrimSpace(destination) == "" {
		return nil, ErrEmptySecretDestination
	}
	return &SecretRequest{ID: id, Label: label, Destination: destination}, nil
}

// ConversationEntry devolve o que entra no histórico enviado ao modelo. Nunca
// contém o valor — só o registro de que ele foi fornecido.
func (s *SecretRequest) ConversationEntry() string {
	if !s.Fulfilled {
		return fmt.Sprintf("[aguardando segredo: %s, destino %s]", s.Label, s.Destination)
	}
	return fmt.Sprintf("[segredo fornecido: %s, destino %s]", s.Label, s.Destination)
}

// Fulfill marca como atendido. Recebe o valor apenas para validar que não veio
// vazio, e o descarta imediatamente — nada é guardado.
func (s *SecretRequest) Fulfill(value string) error {
	if value == "" {
		return errors.New("valor vazio")
	}
	s.Fulfilled = true
	return nil
}

// redactionPlaceholder é o que substitui um segredo encontrado em texto.
const redactionPlaceholder = "[REDIGIDO]"

// Redact remove ocorrências literais dos valores sigilosos de um texto antes
// de ele ir para o modelo, para um log ou para a tela.
//
// É uma segunda linha de defesa, não a primeira. A primeira é o valor nunca
// passar pelo agente; esta existe porque um segredo pode reaparecer por vias
// indiretas — ecoado pelo shell, impresso numa mensagem de erro, presente no
// HTML de uma página. Casos assim já aconteceram com credencial em linha de
// comando aparecendo em `ps`.
//
// Valores com menos de 4 caracteres são ignorados de propósito: redigir uma
// cadeia curta destruiria o texto inteiro sem proteger nada de real.
func Redact(text string, secrets []string) string {
	out := text
	for _, s := range secrets {
		if len(s) < 4 {
			continue
		}
		out = strings.ReplaceAll(out, s, redactionPlaceholder)
	}
	return out
}
