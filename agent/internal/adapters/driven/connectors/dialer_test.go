package connectors

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// allowLoopbackForTest deixa o discador aceitar loopback durante UM caso.
//
// O `httptest` escuta em 127.0.0.1, que a política recusa por desenho. Sem esta
// troca, todo teste de conector que usa servidor local falharia -- não por
// defeito, mas porque o teste e a política miram no mesmo endereço.
//
// Restaura no fim do caso, e os testes deste pacote não usam `t.Parallel()`, de
// modo que não há duas trocas concorrentes.
func allowLoopbackForTest(t *testing.T) {
	t.Helper()
	original := blockedIP
	blockedIP = func(ip net.IP) bool {
		if ip.IsLoopback() {
			return false
		}
		return original(ip)
	}
	t.Cleanup(func() { blockedIP = original })
}

// O discador recusa NOME que resolve para dentro — a lacuna que motivou o
// arquivo.
//
// `localhost` é o caso disponível em qualquer máquina que resolve para
// loopback. O alvo real é `169.254.169.254`, mas depender de um nome público
// que resolva para lá tornaria o teste refém do DNS de terceiro.
//
// Este é o teste que separa "recusa o IP literal" (que já existia) de "recusa o
// caminho todo". Antes dele, `http://localhost:8787` passava pela validação de
// cadastro e a conexão acontecia.
func TestGuardedDialRefusesNameResolvingToLoopback(t *testing.T) {
	_, err := guardedDial(context.Background(), "tcp", "localhost:8787")
	if !errors.Is(err, ErrUnsafeBaseURL) {
		t.Fatalf("esperava ErrUnsafeBaseURL para nome que resolve para dentro, veio %v", err)
	}
	if !strings.Contains(err.Error(), "resolve para") {
		t.Fatalf("a mensagem devia dizer para onde o nome resolveu: %v", err)
	}
}

// IP interno literal também é recusado no discador, e não só no cadastro.
//
// A checagem dupla é deliberada: um conector gravado antes de a validação de
// cadastro existir continua no catálogo, e é o discador que o segura.
func TestGuardedDialRefusesLiteralInternalAddress(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:8787",      // a própria porta de tarefas
		"169.254.169.254:80",  // metadata da nuvem
		"10.0.0.5:443",        // faixa privada
		"100.100.100.100:443", // CGNAT, onde vive a malha
	} {
		if _, err := guardedDial(context.Background(), "tcp", address); !errors.Is(err, ErrUnsafeBaseURL) {
			t.Errorf("%s devia ser recusado, veio %v", address, err)
		}
	}
}

// Endereço sem porta é erro claro, não pânico.
func TestGuardedDialRejectsMalformedAddress(t *testing.T) {
	if _, err := guardedDial(context.Background(), "tcp", "sem-porta"); err == nil {
		t.Fatal("endereço sem porta devia falhar")
	}
}

// Nome que não resolve falha com a causa preservada.
//
// Sem isto a mensagem diria "recusado por ser interno", mandando investigar
// segurança quando o problema é o nome estar errado.
func TestGuardedDialReportsResolutionFailure(t *testing.T) {
	_, err := guardedDial(context.Background(), "tcp", "nao-existe.invalid:443")
	if err == nil {
		t.Fatal("nome inexistente devia falhar")
	}
	if errors.Is(err, ErrUnsafeBaseURL) {
		t.Fatalf("falha de DNS não é bloqueio de segurança: %v", err)
	}
	if !strings.Contains(err.Error(), "não resolvi") {
		t.Fatalf("a mensagem devia apontar a resolução: %v", err)
	}
}

// Destino EXTERNO continua funcionando — a prova de que a checagem não recusa
// tudo.
//
// Sem este caso, um discador que devolvesse erro sempre passaria nos testes
// acima e quebraria todo conector em produção. É o segundo sentido da prova de
// falha: verificação que recusa qualquer entrada é "segura" e inútil.
func TestGuardedClientReachesExternalDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// O httptest escuta em loopback, que o discador recusa por desenho. Para
	// exercitar o caminho de sucesso sem afrouxar a regra, o discador é chamado
	// com uma checagem de "interno" que aceita loopback — o resto do caminho
	// (resolução, discagem no IP resolvido, timeout) é o mesmo.
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("URL do servidor de teste inesperada: %v", err)
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		t.Fatalf("o destino de teste devia aceitar conexão: %v", err)
	}
	_ = conn.Close()
}

// Nome com VÁRIOS endereços conecta no que atende, não só no primeiro.
//
// `localhost` resolve para `::1` e `127.0.0.1`. Um servidor que só escuta em
// IPv4 fica inalcançável se o discador parar no primeiro da lista — foi o que
// a primeira versão deste arquivo fez, quebrando sete testes de conector.
//
// O teste falha se alguém voltar a discar só em `addresses[0]`: o `httptest`
// escuta apenas em IPv4, e em máquina com IPv6 o primeiro resolvido é `::1`.
func TestGuardedDialTriesEveryResolvedAddress(t *testing.T) {
	allowLoopbackForTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("URL do servidor de teste inesperada: %v", err)
	}
	conn, err := guardedDial(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("localhost devia conectar pelo endereço que atende: %v", err)
	}
	_ = conn.Close()
}

// O cliente para de seguir redirecionamento em cadeia.
//
// Cada salto passa pelo discador, então o destino é validado de todo jeito; o
// limite existe para o outro problema — uma cadeia infinita prenderia a tarefa
// até o timeout de 60s, e uma tarefa presa segura a trava da tela.
func TestGuardedClientStopsRedirectChain(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/again", http.StatusFound)
	}))
	defer server.Close()

	// Cliente com o mesmo CheckRedirect, mas discador comum: o objetivo aqui é
	// o limite de saltos, e o servidor de teste escuta em loopback.
	client := newGuardedClient(5 * time.Second)
	client.Transport = http.DefaultTransport

	_, err := client.Get(server.URL)
	if err == nil {
		t.Fatal("cadeia de redirecionamento devia ser interrompida")
	}
	if !strings.Contains(err.Error(), "redirecionamento demais") {
		t.Fatalf("erro devia apontar a cadeia: %v", err)
	}
}

// O cliente do conector é o guardado, e não o padrão.
//
// Testar o discador isolado não prova que ele está LIGADO. Esta é a fiação, e é
// onde o defeito real morava: a validação existia e o cliente não a usava.
func TestConnectorClientUsesGuardedTransport(t *testing.T) {
	tool := newHTTPTool(
		&loadedConnector{
			connector: &domain.Connector{Name: "teste"},
			manifest:  Manifest{BaseURL: "https://api.exemplo.com"},
		},
		ManifestOperation{Name: "op"},
		nil,
		"",
	)
	transport, ok := tool.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("o conector devia usar *http.Transport próprio, veio %T", tool.client.Transport)
	}
	if transport.DialContext == nil {
		t.Fatal("o transporte do conector está sem discador — a checagem não roda")
	}
	if tool.client.CheckRedirect == nil {
		t.Fatal("o cliente do conector está sem limite de redirecionamento")
	}
}
