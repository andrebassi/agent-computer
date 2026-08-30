package connectors

import (
	"errors"
	"testing"
)

// O metadata da nuvem é recusado — é o endereço que motiva a checagem.
//
// Prova que a proteção enxerga: com validateBaseURL neutralizada, este teste
// reprova. É o caso que devolveria ao modelo a configuração da máquina, e em
// alguns provedores a credencial de instância junto.
func TestRejectsCloudMetadataAddress(t *testing.T) {
	if err := validateBaseURL("http://169.254.169.254/metadata/v1/"); !errors.Is(err, ErrUnsafeBaseURL) {
		t.Fatalf("o metadata da nuvem devia ser recusado, veio %v", err)
	}
}

// Loopback e faixa privada também são recusados.
//
// A máquina roda o Chrome com depuração remota e a porta de tarefas em
// 127.0.0.1, ambos sem autenticação de rede: um conector apontado para lá seria
// caminho de volta para dentro, com a credencial dele anexada.
func TestRejectsInternalTargets(t *testing.T) {
	internal := []string{
		"http://127.0.0.1:9221/json",
		"http://localhost:8787/tasks",
		"http://[::1]:8787/",
		"http://10.0.0.5/",
		"http://192.168.1.10/",
		"http://172.16.0.1/",
		"http://100.64.0.1/",
		"http://0.0.0.0/",
	}
	for _, raw := range internal {
		err := validateBaseURL(raw)
		if raw == "http://localhost:8787/tasks" {
			// `localhost` é nome, não IP literal — a checagem não o alcança, e
			// isso está registrado como limite conhecido. O teste trava o
			// comportamento REAL para ninguém achar que está coberto.
			if err != nil {
				t.Fatalf("localhost é nome e passa por desenho, veio %v", err)
			}
			continue
		}
		if !errors.Is(err, ErrUnsafeBaseURL) {
			t.Fatalf("%s devia ser recusado, veio %v", raw, err)
		}
	}
}

// Esquema fora de http/https é recusado.
//
// `file://` leria arquivo do disco pelo cliente HTTP, e `gopher://` foi por anos
// o caminho clássico para falar com serviço binário interno a partir de um fetch
// que parecia inocente.
func TestRejectsNonHTTPSchemes(t *testing.T) {
	for _, raw := range []string{"file:///etc/shadow", "gopher://127.0.0.1:6379/", "ftp://exemplo.com/"} {
		if err := validateBaseURL(raw); !errors.Is(err, ErrUnsafeBaseURL) {
			t.Fatalf("%s devia ser recusado, veio %v", raw, err)
		}
	}
}

// Endereço público legítimo passa.
//
// A metade que importa tanto quanto a outra: uma checagem que recusasse tudo
// seria "segura" e inútil, e a primeira coisa que alguém faria seria desligá-la.
func TestAcceptsPublicEndpoints(t *testing.T) {
	for _, raw := range []string{
		"https://api.exemplo.com/v1",
		"https://api.open-meteo.com",
		"http://8.8.8.8/",
		"https://exemplo.com:8443/base",
	} {
		if err := validateBaseURL(raw); err != nil {
			t.Fatalf("%s devia passar, veio %v", raw, err)
		}
	}
}

// Endereço malformado ou sem host é recusado.
func TestRejectsMalformedBaseURL(t *testing.T) {
	for _, raw := range []string{"", "http://", "não é url", "://sem-esquema"} {
		if err := validateBaseURL(raw); err == nil {
			t.Fatalf("%q devia ser recusado", raw)
		}
	}
}
