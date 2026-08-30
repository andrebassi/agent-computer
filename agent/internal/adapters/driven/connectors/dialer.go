package connectors

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// dialTimeout limita a espera por conexão. Curto de propósito: um destino
// interno que não responde travaria a tarefa pelo tempo do timeout de leitura,
// e o interessante aqui é falhar rápido.
const dialTimeout = 10 * time.Second

// guardedDial abre a conexão SÓ depois de conferir o IP de destino.
//
// # Por que no discador, e não na URL
//
// `validateBaseURL` recusa `http://169.254.169.254`, mas deixa passar um NOME
// que resolve para lá — e essa lacuna não se fecha resolvendo o nome no
// cadastro. Três caminhos escapam de qualquer validação feita antes:
//
//  1. o DNS responde outra coisa na hora da chamada (rebinding). O cadastro viu
//     um IP público; a conexão vai para 169.254.169.254;
//  2. um redirect 302 leva o cliente para um destino que ninguém validou —
//     `validateBaseURL` olha a URL cadastrada, não a que o servidor mandou
//     seguir;
//  3. o nome tem vários registros e o resolvedor devolve um interno na segunda
//     tentativa.
//
// O discador é o único ponto por onde os três passam obrigatoriamente. Ele vê o
// IP FINAL, depois da resolução e de cada redirect, no instante de abrir o
// socket — não há janela entre a checagem e o uso.
//
// # O que continua fora do alcance
//
// A ferramenta de shell alcança a rede interna diretamente, e nada aqui a
// limita. O que este bloqueio fecha é o caminho que o agentd percorre EM NOME
// do modelo, dentro do processo que tem o cofre aberto — onde a credencial do
// conector seria anexada à requisição.

// blockedIP decide se um IP está fora dos limites.
//
// É variável, e não chamada direta a `isInternal`, por um motivo só: o servidor
// de teste (`httptest`) escuta em loopback, que é justamente o que a política
// recusa. Sem um ponto de troca, testar o caminho de sucesso exigiria um
// servidor externo de verdade -- e teste que depende de rede alheia falha por
// motivo que não é defeito.
//
// A política de PRODUÇÃO não muda: quem não trocar explicitamente pega
// `isInternal`. A troca vive em `allowLoopbackForTest`, no arquivo de teste, e
// restaura o valor no fim de cada caso.
var blockedIP = isInternal

// guardedDial valida o destino e só então abre o socket. Ver a nota acima.
func guardedDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("endereço inválido %q: %w", address, err)
	}

	// A resolução é feita aqui, e é o resultado DELA que vai para o socket.
	// Resolver e depois deixar o `net.Dial` resolver de novo reabriria a janela
	// entre a checagem e o uso: a segunda consulta pode devolver outro IP.
	resolver := &net.Resolver{}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("não resolvi %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: %q não resolveu para endereço nenhum", ErrUnsafeBaseURL, host)
	}

	// UM endereço interno reprova a conexão inteira, em vez de a função pular
	// para o próximo da lista. Um nome que devolve um IP público e um interno é
	// exatamente a forma do ataque -- aceitar o público deixaria o atacante
	// repetir até o resolvedor entregar o interno primeiro.
	for _, candidate := range addresses {
		if blockedIP(candidate.IP) {
			return nil, fmt.Errorf("%w: %s resolve para %s", ErrUnsafeBaseURL, host, candidate.IP)
		}
	}

	dialer := &net.Dialer{Timeout: dialTimeout}
	_, port, _ := net.SplitHostPort(address)

	// Tenta TODOS os endereços resolvidos, na ordem, como faz o discador padrão.
	//
	// Parar no primeiro parece equivalente e não é: um nome com registro AAAA e
	// A resolve primeiro para IPv6, e um serviço que só escuta em IPv4 fica
	// inalcançável. Foi assim que a primeira versão deste arquivo quebrou sete
	// testes de conector -- `localhost` resolve para `::1` e `127.0.0.1`, e o
	// servidor de teste só atende no segundo.
	//
	// Todos já passaram pela checagem acima, então tentar o resto não afrouxa
	// nada: um endereço interno teria reprovado a conexão inteira antes daqui.
	var lastErr error
	for _, candidate := range addresses {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("nenhum endereço de %q aceitou conexão: %w", host, lastErr)
}

// newGuardedClient devolve o cliente HTTP que os conectores usam.
//
// O transporte é próprio, e não o `http.DefaultTransport`: mexer no padrão
// afetaria toda chamada HTTP do processo, inclusive a do modelo, e a intenção é
// restringir só o que sai em nome do conector.
func newGuardedClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext: guardedDial,
		// Herdados do padrão porque o valor zero é pior: sem eles cada requisição
		// abriria conexão nova e o TLS seria renegociado a cada chamada.
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// Cada salto de redirect passa pelo discador acima, então o destino
		// final é validado do mesmo jeito. O limite existe para o outro
		// problema: uma cadeia infinita prenderia a tarefa até o timeout.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("redirecionamento demais (%d saltos) até %s", len(via), req.URL.Host)
			}
			return nil
		},
	}
}
