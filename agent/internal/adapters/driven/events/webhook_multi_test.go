package events

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// httpRecorder guarda o que um destino recebeu, para o teste comparar formatos.
type httpRecorder struct {
	mu     sync.Mutex
	bodies []string
	status int
}

// handler devolve o servidor que grava o corpo e responde o status combinado.
func (r *httpRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(raw))
		r.mu.Unlock()
		status := r.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	}
}

// count devolve quantos avisos chegaram.
func (r *httpRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// last devolve o último corpo recebido.
func (r *httpRecorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return ""
	}
	return r.bodies[len(r.bodies)-1]
}

// Dois destinos, formatos diferentes, o MESMO aviso.
//
// É a razão de o multi existir: o ntfy leva texto ao celular de quem precisa
// agir, e o coletor guarda o JSON cru com os campos, que é o que serve para
// depurar. Escolher um perderia o outro.
func TestMultiSinkDeliversEachInItsOwnFormat(t *testing.T) {
	celular, coletor := &httpRecorder{}, &httpRecorder{}
	srvNtfy := httptest.NewServer(celular.handler())
	defer srvNtfy.Close()
	srvRaw := httptest.NewServer(coletor.handler())
	defer srvRaw.Close()

	sink, err := NewMultiSink("ntfy="+srvNtfy.URL+",raw="+srvRaw.URL, FormatRaw)
	if err != nil {
		t.Fatalf("montando destinos: %v", err)
	}
	if sink.Destinations() != 2 {
		t.Fatalf("deviam ser 2 destinos, são %d", sink.Destinations())
	}
	if err := sink.Publish(context.Background(), blockedEvent()); err != nil {
		t.Fatalf("publicando: %v", err)
	}

	if strings.HasPrefix(strings.TrimSpace(celular.last()), "{") {
		t.Errorf("o destino ntfy devia receber texto: %s", celular.last())
	}
	if !strings.Contains(coletor.last(), `"task_id"`) {
		t.Errorf("o destino raw devia receber o JSON cru: %s", coletor.last())
	}
}

// Um destino fora do ar NÃO impede o outro, e não segura a fila.
//
// Exigir que todos aceitem parece mais rigoroso e é pior: um destino
// permanentemente quebrado faria o destino bom receber a mesma notificação a
// cada passada do timer, até quem recebe silenciar o canal.
func TestMultiSinkSucceedsWhenAtLeastOneAccepts(t *testing.T) {
	bom, quebrado := &httpRecorder{}, &httpRecorder{status: http.StatusInternalServerError}
	srvBom := httptest.NewServer(bom.handler())
	defer srvBom.Close()
	srvRuim := httptest.NewServer(quebrado.handler())
	defer srvRuim.Close()

	sink, _ := NewMultiSink(srvRuim.URL+","+srvBom.URL, FormatRaw)
	err := sink.Publish(context.Background(), blockedEvent())

	var partial *PartialDelivery
	if !errors.As(err, &partial) {
		t.Fatalf("devia sinalizar entrega parcial, veio %v", err)
	}
	if partial.Delivered != 1 {
		t.Errorf("um destino devia ter recebido, contou %d", partial.Delivered)
	}
	if bom.count() != 1 {
		t.Errorf("o destino saudável devia ter recebido o aviso")
	}
	if !strings.Contains(partial.Error(), "500") {
		t.Errorf("a mensagem devia dizer o que houve com o destino quebrado: %s", partial.Error())
	}
}

// TODOS fora do ar é falha de verdade: a fila precisa segurar o aviso.
func TestMultiSinkFailsWhenNobodyAccepts(t *testing.T) {
	quebrado := &httpRecorder{status: http.StatusBadGateway}
	srv := httptest.NewServer(quebrado.handler())
	defer srv.Close()

	sink, _ := NewMultiSink(srv.URL, FormatRaw)
	err := sink.Publish(context.Background(), blockedEvent())
	if err == nil {
		t.Fatal("sem nenhuma entrega, devia falhar")
	}
	var partial *PartialDelivery
	if errors.As(err, &partial) {
		t.Fatal("falha total não é entrega parcial — a fila seria limpa por engano")
	}
}

// Um destino só continua se comportando como antes.
//
// É a garantia de compatibilidade: toda máquina já configurada tem uma URL
// simples em AGENT_WEBHOOK, e ela não pode mudar de comportamento.
func TestSingleDestinationKeepsWorking(t *testing.T) {
	destino := &httpRecorder{}
	srv := httptest.NewServer(destino.handler())
	defer srv.Close()

	sink, err := NewMultiSink(srv.URL, FormatNtfy)
	if err != nil {
		t.Fatalf("montando destino: %v", err)
	}
	if sink.Destinations() != 1 {
		t.Fatalf("devia ser 1 destino, são %d", sink.Destinations())
	}
	if err := sink.Publish(context.Background(), blockedEvent()); err != nil {
		t.Fatalf("publicando: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(destino.last()), "{") {
		t.Error("o formato do fallback devia valer para o item sem prefixo")
	}
}

// URL com `=` na query NÃO é confundida com prefixo de formato.
//
// Sem a checagem de formato conhecido, `https://x/in/?a=b` teria "https://x/in/?a"
// tomado por nome de formato, e o destino viraria "b" — lixo silencioso.
func TestQueryStringIsNotMistakenForFormatPrefix(t *testing.T) {
	urls, formats := ParseDestinations("https://exemplo.com/in/?token=abc", FormatNtfy)
	if len(urls) != 1 || urls[0] != "https://exemplo.com/in/?token=abc" {
		t.Fatalf("a URL devia ficar inteira: %v", urls)
	}
	if formats[0] != FormatNtfy {
		t.Errorf("sem prefixo, o formato devia ser o de fallback, veio %q", formats[0])
	}
}

// Espaço em volta, item vazio e vírgula sobrando não quebram a lista.
//
// Configuração é escrita à mão, num arquivo, e vírgula sobrando no fim é o erro
// de digitação mais comum que existe.
func TestDestinationListToleratesSloppyWriting(t *testing.T) {
	urls, formats := ParseDestinations(
		"  ntfy = https://a/x , , raw=https://b/y ,", FormatRaw)
	if len(urls) != 2 {
		t.Fatalf("deviam sobrar 2 destinos, vieram %d: %v", len(urls), urls)
	}
	if urls[0] != "https://a/x" || urls[1] != "https://b/y" {
		t.Errorf("as URLs deviam vir sem espaço: %v", urls)
	}
	if formats[0] != FormatNtfy || formats[1] != FormatRaw {
		t.Errorf("os formatos vieram errados: %v", formats)
	}
}

// Lista vazia é erro na construção, e não no primeiro envio.
func TestEmptyDestinationListIsRejectedEarly(t *testing.T) {
	if _, err := NewMultiSink("   ,  , ", FormatRaw); err == nil {
		t.Fatal("lista sem destino devia ser recusada")
	}
}

// A fila é LIMPA quando a entrega foi parcial.
//
// Este é o ponto do desenho, e o que separa "parcial" de "falhou": se a fila
// segurasse o aviso por causa do destino quebrado, o destino bom receberia a
// mesma notificação a cada 5 minutos, para sempre.
func TestPartialDeliveryStillClearsTheQueue(t *testing.T) {
	bom, quebrado := &httpRecorder{}, &httpRecorder{status: http.StatusInternalServerError}
	srvBom := httptest.NewServer(bom.handler())
	defer srvBom.Close()
	srvRuim := httptest.NewServer(quebrado.handler())
	defer srvRuim.Close()

	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("montando a fila: %v", err)
	}
	if err := spool.Publish(context.Background(), blockedEvent()); err != nil {
		t.Fatalf("enfileirando: %v", err)
	}

	sink, _ := NewMultiSink(srvRuim.URL+","+srvBom.URL, FormatRaw)
	delivered, err := Drain(context.Background(), spool, sink)
	if delivered != 1 {
		t.Errorf("devia contar 1 entregue, contou %d", delivered)
	}
	var partial *PartialDelivery
	if !errors.As(err, &partial) {
		t.Fatalf("devia sinalizar entrega parcial, veio %v", err)
	}

	pending, err := spool.Pending(context.Background())
	if err != nil {
		t.Fatalf("lendo a fila: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("a fila devia estar limpa; sobraram %d — o destino bom receberia duplicata a cada passada",
			len(pending))
	}
}

// E o oposto: com TODOS fora do ar, a fila segura o aviso.
func TestTotalFailureKeepsTheQueue(t *testing.T) {
	quebrado := &httpRecorder{status: http.StatusBadGateway}
	srv := httptest.NewServer(quebrado.handler())
	defer srv.Close()

	spool, _ := NewSpool(t.TempDir())
	_ = spool.Publish(context.Background(), blockedEvent())

	sink, _ := NewMultiSink(srv.URL, FormatRaw)
	if _, err := Drain(context.Background(), spool, sink); err == nil {
		t.Fatal("falha total devia devolver erro")
	}
	pending, _ := spool.Pending(context.Background())
	if len(pending) != 1 {
		t.Errorf("o aviso devia continuar na fila, sobraram %d", len(pending))
	}
}
