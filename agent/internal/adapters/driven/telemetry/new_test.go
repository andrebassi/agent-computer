package telemetry

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// TestNewWithEndpointDoesNotBlockOnDial é o teste mais importante deste pacote.
//
// Ele prova a propriedade de que tudo depende: **o backend estar fora do ar não
// pode impedir o agente de subir**. O Mac que hospeda o backend é um laptop, e
// ele vai estar fechado — se `New` bloqueasse esperando conexão, o `agentd` não
// subiria justamente nas horas em que ninguém está olhando.
//
// O endereço aponta para uma porta fechada de propósito. Sem a ausência de
// `WithBlocking` no adaptador, esta chamada penduraria até o prazo do gRPC, e o
// teste estouraria o tempo em vez de falhar com mensagem clara.
func TestNewWithEndpointDoesNotBlockOnDial(t *testing.T) {
	// Porta fechada de verdade: abre, descobre o número, e fecha antes de usar.
	// Escolher um número "provavelmente livre" à mão produz teste que falha na
	// máquina de quem por acaso usa aquela porta.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("não consegui reservar uma porta: %v", err)
	}
	closedAddress := listener.Addr().String()
	_ = listener.Close()

	done := make(chan struct{})
	var tracer *Tracer
	var shutdown Shutdown

	go func() {
		defer close(done)
		tracer, shutdown, err = New(context.Background(), closedAddress, "agentd", "0.0.0")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("New bloqueou com o backend fora do ar; o agente não subiria")
	}

	if err != nil {
		t.Fatalf("backend fora do ar virou erro de criação: %v", err)
	}
	if tracer == nil {
		t.Fatal("com endpoint configurado o rastreador não pode ser nil")
	}

	// E o caminho de uso também não pode bloquear: o trecho é enfileirado, e
	// quem entrega é o lote, noutra goroutine.
	_, span := tracer.Start(context.Background(), "agentd.task", service.String("id", "task-1"))
	span.SetAttributes(service.Int("turns", 1))
	span.End(nil)

	// O encerramento tem prazo próprio. Sem contexto com prazo, ele esperaria o
	// exportador desistir sozinho — que é exatamente o que não pode acontecer
	// enquanto o systemd conta os 40 segundos até o SIGKILL.
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout())
	defer cancel()
	// O erro aqui é esperado e aceito: não há para onde enviar. O que se prova é
	// que ele RETORNA, em vez de pendurar.
	_ = shutdown(ctx)
}

// TestNewWithInvalidAddressReturnsError cobre o ramo de falha da criação.
//
// Importa porque o chamador trata erro e sucesso de formas diferentes: no erro
// ele avisa em stderr e segue com o rastreador mudo. Se `New` nunca devolvesse
// erro, esse caminho seria código morto — e código morto num caminho de falha só
// se descobre no dia em que ele precisava ter funcionado.
func TestNewWithInvalidAddressReturnsError(t *testing.T) {
	// O caractere nulo torna o endereço impossível de resolver em qualquer
	// plataforma, sem depender de DNS nem de rede.
	_, _, err := New(context.Background(), "\x00invalid-address", "agentd", "0.0.0")
	if err == nil {
		t.Fatal("endereço inválido não produziu erro; o caminho de degradação nunca roda")
	}
}
