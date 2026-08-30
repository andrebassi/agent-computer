package domain

import (
	"reflect"
	"strings"
	"testing"
)

// A garantia central do secret request é estrutural, não comportamental: o tipo
// não pode ter campo capaz de guardar o valor. Este teste falha se alguém
// acrescentar um — é a única forma de a promessa "não entra na conversa"
// sobreviver a uma refatoração distraída.
func TestSecretRequestHasNoFieldForTheValue(t *testing.T) {
	proibidos := map[string]bool{
		"value": true, "secret": true, "password": true,
		"token": true, "code": true, "content": true,
	}
	typ := reflect.TypeOf(SecretRequest{})
	for i := 0; i < typ.NumField(); i++ {
		nome := strings.ToLower(typ.Field(i).Name)
		if proibidos[nome] {
			t.Fatalf("campo %q guardaria o segredo no struct — ele nunca deve ser retido", typ.Field(i).Name)
		}
	}
}

// Pedido sem descrição ou sem destino é a forma exata de um golpe: um campo
// dizendo "digite o valor", sem dizer qual nem para onde vai.
func TestNewSecretRequestRequiresLabelAndDestination(t *testing.T) {
	cases := []struct {
		name        string
		label       string
		destination string
	}{
		{"sem descrição", "", "painel.exemplo.com"},
		{"descrição só de espaços", "   ", "painel.exemplo.com"},
		{"sem destino", "senha", ""},
		{"destino só de espaços", "senha", "  "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewSecretRequest("s1", c.label, c.destination); err == nil {
				t.Fatalf("esperava erro para %s", c.name)
			}
		})
	}
}

// O que entra no histórico precisa dizer o que foi pedido e para onde, sem
// jamais conter o valor.
func TestConversationEntryNeverLeaksValue(t *testing.T) {
	req, err := NewSecretRequest("s1", "senha do painel", "painel.exemplo.com")
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	antes := req.ConversationEntry()
	if !strings.Contains(antes, "aguardando") {
		t.Fatalf("entrada devia indicar espera: %q", antes)
	}
	if err := req.Fulfill("senha-secreta-do-andre"); err != nil {
		t.Fatalf("fulfill falhou: %v", err)
	}
	depois := req.ConversationEntry()
	if strings.Contains(depois, "senha-secreta-do-andre") {
		t.Fatalf("o valor VAZOU para o histórico: %q", depois)
	}
	if !strings.Contains(depois, "fornecido") {
		t.Fatalf("entrada devia registrar que foi fornecido: %q", depois)
	}
	if !strings.Contains(depois, "painel.exemplo.com") {
		t.Fatalf("entrada devia manter o destino: %q", depois)
	}
}

// Valor vazio não conta como atendido: marcaria o pedido como resolvido e o
// agente seguiria adiante sem o dado.
func TestFulfillRejectsEmptyValue(t *testing.T) {
	req, err := NewSecretRequest("s1", "senha", "destino")
	if err != nil {
		t.Fatalf("criação falhou: %v", err)
	}
	if err := req.Fulfill(""); err == nil {
		t.Fatal("valor vazio devia ser recusado")
	}
	if req.Fulfilled {
		t.Fatal("pedido não devia ficar marcado como atendido")
	}
}

// A limpeza é a segunda linha de defesa, para o caso de o segredo reaparecer
// ecoado por um shell ou dentro do HTML de uma página.
func TestRedactRemovesSecretsFromText(t *testing.T) {
	texto := "conectando com senha hunter2000 no host x"
	got := Redact(texto, []string{"hunter2000"})
	if strings.Contains(got, "hunter2000") {
		t.Fatalf("segredo sobreviveu: %q", got)
	}
	if !strings.Contains(got, "[REDIGIDO]") {
		t.Fatalf("faltou o marcador de redação: %q", got)
	}
}

// Valor curto é ignorado de propósito: apagar uma cadeia de 2 ou 3 caracteres
// destruiria o texto inteiro sem proteger nada de real.
func TestRedactIgnoresShortValues(t *testing.T) {
	texto := "o valor de a e b esta ok"
	got := Redact(texto, []string{"a", "b", "ok"})
	if got != texto {
		t.Fatalf("texto não devia mudar por causa de valores curtos: %q", got)
	}
}

// Várias ocorrências e vários segredos no mesmo texto — o caso realista, já que
// um comando costuma repetir a credencial.
func TestRedactHandlesMultipleOccurrences(t *testing.T) {
	texto := "user=admin pass=segredo123 retry pass=segredo123 token=abcd1234"
	got := Redact(texto, []string{"segredo123", "abcd1234"})
	if strings.Contains(got, "segredo123") || strings.Contains(got, "abcd1234") {
		t.Fatalf("sobrou segredo: %q", got)
	}
	if n := strings.Count(got, "[REDIGIDO]"); n != 3 {
		t.Fatalf("esperava 3 marcadores, veio %d: %q", n, got)
	}
}

// Sem segredos registrados, o texto passa intacto — a função não pode danificar
// conteúdo normal.
func TestRedactWithoutSecretsIsIdentity(t *testing.T) {
	texto := "nada a esconder aqui"
	if got := Redact(texto, nil); got != texto {
		t.Fatalf("texto foi alterado sem necessidade: %q", got)
	}
}
