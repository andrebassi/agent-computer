package connectors

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrUnsafeBaseURL indica endereço de conector que aponta para dentro da rede.
var ErrUnsafeBaseURL = errors.New("base_url aponta para a própria máquina ou rede interna")

// validateBaseURL recusa endereço de conector que aponta para dentro.
//
// # O alvo
//
// O caso que motiva é `169.254.169.254`, o serviço de metadata da nuvem. Um
// conector apontado para lá devolve ao modelo a configuração da máquina — e, em
// provedores que expõem credencial de instância por esse caminho, a credencial
// junto. O endereço não parece perigoso: é um IP comum numa faixa que ninguém
// olha duas vezes.
//
// Loopback e faixa privada entram pelo mesmo motivo: a máquina roda o Chrome com
// depuração remota e a própria porta HTTP de tarefas, ambos em 127.0.0.1 sem
// autenticação de rede. Um conector apontado para lá seria um caminho de volta
// para dentro, com a credencial do conector anexada.
//
// # O que isto NÃO resolve
//
// A ferramenta de shell alcança tudo isso diretamente — este bloqueio não a
// limita. O que ele fecha é o caminho que o agentd percorre EM NOME do modelo,
// dentro do processo que tem o cofre aberto.
//
// # Por que só o host literal
//
// Um nome que resolve para 169.254.169.254 passa por aqui. Resolver no cadastro
// não ajudaria: o DNS pode responder outra coisa na hora da chamada, e um
// bloqueio que parece cobrir e não cobre é pior que a ausência dele. Fechar de
// verdade exigiria um discador que valide o IP no momento da conexão — está
// registrado como aberto em docs/SECURITY.md.
func validateBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("base_url inválida: %w", err)
	}
	// Só http e https. Um `file://` leria arquivo do disco pelo cliente HTTP, e
	// `gopher://` foi por anos o caminho clássico para falar com serviço binário
	// interno a partir de um fetch inocente.
	switch parsed.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("%w: esquema %q não é http nem https", ErrUnsafeBaseURL, parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%w: sem host", ErrUnsafeBaseURL)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Nome, não IP. Passa — ver a nota sobre DNS acima.
		return nil
	}
	// `blockedIP`, e não `isInternal` direto: a política de bloqueio é UMA, e o
	// cadastro tem de julgar exatamente como o discador. Duas cópias do mesmo
	// julgamento divergem na primeira vez que uma faixa for acrescentada só num
	// dos lados -- e a que ficar para trás vira o buraco.
	if blockedIP(ip) {
		return fmt.Errorf("%w: %s", ErrUnsafeBaseURL, ip)
	}
	return nil
}

// isInternal diz se o IP fica dentro da máquina ou da rede local.
//
// Inclui a faixa 169.254.0.0/16 por inteiro, e não só o endereço do metadata:
// os provedores usam vizinhos dela para outros serviços internos, e listar um
// endereço só deixaria o resto aberto.
func isInternal(ip net.IP) bool {
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return true
	case ip.IsUnspecified(), ip.IsInterfaceLocalMulticast():
		return true
	}
	// Compartilhada entre provedores (CGNAT, 100.64.0.0/10). Não é coberta por
	// IsPrivate e é onde vive a malha Tailscale desta máquina.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}
