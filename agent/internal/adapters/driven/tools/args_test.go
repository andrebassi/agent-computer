package tools

import (
	"context"
	"strings"
	"testing"
)

// Campo com nome errado é RECUSADO, em vez de virar campo vazio.
//
// É o defeito que motiva o arquivo. `{"comand":"ls"}` — o erro de digitação que
// um modelo comete — decodificava sem erro, deixava `Command` vazio, e a
// ferramenta respondia "comando vazio". A mensagem mandava investigar a coisa
// errada, e o modelo tendia a repetir a chamada em vez de olhar o nome do campo.
func TestShellRefusesMisspelledField(t *testing.T) {
	shell := NewShellSandboxed("/tmp", NewSandbox(""))
	result, err := shell.Execute(context.Background(), 1, `{"comand":"ls"}`)
	if err != nil {
		t.Fatalf("não devia devolver erro de execução: %v", err)
	}
	if !result.Failed {
		t.Fatal("campo desconhecido devia marcar falha")
	}
	if !strings.Contains(result.Output, "comand") {
		t.Errorf("a mensagem devia nomear o campo desconhecido: %s", result.Output)
	}
	if strings.Contains(result.Output, "comando vazio") {
		t.Errorf("a mensagem antiga mandava investigar a coisa errada: %s", result.Output)
	}
}

// Campo correto continua funcionando — o outro sentido.
//
// Sem este caso, uma validação quebrada que recusasse tudo passaria no de cima.
func TestShellAcceptsTheDeclaredField(t *testing.T) {
	shell := NewShellSandboxed("/tmp", NewSandbox(""))
	result, err := shell.Execute(context.Background(), 1, `{"command":"echo funciona"}`)
	if err != nil {
		t.Fatalf("execução: %v", err)
	}
	if result.Failed {
		t.Fatalf("comando válido não devia falhar: %s", result.Output)
	}
	if !strings.Contains(result.Output, "funciona") {
		t.Errorf("a saída devia voltar: %s", result.Output)
	}
}

// Argumento VAZIO não é recusado.
//
// Ferramenta sem parâmetro obrigatório é chamada com `{}` ou com nada, e
// recusar aqui quebraria chamada legítima.
func TestEmptyArgumentsAreAccepted(t *testing.T) {
	type vazio struct {
		Campo string `json:"campo"`
	}
	for _, entrada := range []string{"", "   ", "{}"} {
		var alvo vazio
		if err := decodeArgs(entrada, &alvo); err != nil {
			t.Errorf("entrada %q não devia ser recusada: %v", entrada, err)
		}
	}
}

// JSON malformado continua sendo recusado, com a causa.
func TestMalformedJSONIsStillRefused(t *testing.T) {
	type alvo struct {
		Campo string `json:"campo"`
	}
	var destino alvo
	err := decodeArgs(`{"campo": `, &destino)
	if err == nil {
		t.Fatal("JSON truncado devia ser recusado")
	}
	if !strings.Contains(err.Error(), "argumentos inválidos") {
		t.Errorf("a mensagem devia dizer o que houve: %v", err)
	}
}

// Tipo errado num campo declarado também é recusado.
//
// Não é o alvo principal do arquivo, mas vem de graça com o decodificador — e a
// mensagem do Go nomeia o campo e o tipo esperado, que é o que o modelo precisa.
func TestWrongTypeIsRefused(t *testing.T) {
	type alvo struct {
		Numero int `json:"numero"`
	}
	var destino alvo
	if err := decodeArgs(`{"numero":"texto"}`, &destino); err == nil {
		t.Fatal("tipo errado devia ser recusado")
	}
}
