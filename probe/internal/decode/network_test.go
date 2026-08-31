package decode

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

// buildRawNetEvent monta um registro com o layout que net.bpf.c emite.
//
// Offsets literais, tirados do arquivo em C — não de uma struct Go, que seria o
// mesmo palpite que se quer verificar.
func buildRawNetEvent(family uint16, destination, source [4]byte, destinationPort, sourcePort uint16, comm string) []byte {
	raw := make([]byte, NetEventSize)
	binary.LittleEndian.PutUint64(raw[0:8], 1_788_000_000_000)
	binary.LittleEndian.PutUint64(raw[8:16], 4026531841)
	binary.LittleEndian.PutUint32(raw[16:20], 4242)
	binary.LittleEndian.PutUint32(raw[20:24], 4200)
	binary.LittleEndian.PutUint32(raw[24:28], 1000)
	copy(raw[28:32], destination[:])
	copy(raw[32:36], source[:])
	// As portas vão em ORDEM DE HOST, que é como o kernel as entrega: o
	// tracepoint já fez `ntohs()`, e o `print fmt` dele usa `%hu`.
	//
	// A primeira versão deste helper usava big-endian, e o teste PASSAVA — ele
	// concordava com o mesmo erro que o decodificador cometia. Só a execução na
	// máquina denunciou, mostrando a porta 443 como 47873. É o lembrete de que
	// um teste escrito pela mesma cabeça que escreveu o código herda a suposição
	// dela; contrato de ABI se confere no arquivo `format` do kernel, não na
	// memória.
	binary.LittleEndian.PutUint16(raw[36:38], destinationPort)
	binary.LittleEndian.PutUint16(raw[38:40], sourcePort)
	binary.LittleEndian.PutUint16(raw[40:42], family)
	copy(raw[44:44+commLength], comm)
	return raw
}

// TestNetEventSizeMatchesContract trava o tamanho do registro.
//
// O valor não é a soma dos campos (76): a struct contém `__u64`, então alinha em
// 8 e o compilador arredonda para 80. Errar isso faria o leitor recusar TODO
// evento como curto — falha total, silenciosa do lado do kernel.
func TestNetEventSizeMatchesContract(t *testing.T) {
	const expected = 80
	if NetEventSize != expected {
		t.Fatalf("NetEventSize = %d, contrato com net.bpf.c diz %d", NetEventSize, expected)
	}
}

// TestDecodeNetReadsEveryField prova que cada campo sai do offset certo.
//
// Portas e endereços DIFERENTES entre si de propósito: com valores iguais, uma
// troca entre origem e destino passaria despercebida — e é a troca mais fácil
// de cometer, porque os campos são vizinhos e do mesmo tipo.
func TestDecodeNetReadsEveryField(t *testing.T) {
	raw := buildRawNetEvent(familyIPv4,
		[4]byte{93, 184, 216, 34}, // destino
		[4]byte{10, 0, 0, 5},      // origem
		443, 51234, "curl")

	event, err := DecodeNet(raw)
	if err != nil {
		t.Fatalf("decodificação falhou: %v", err)
	}

	cases := []struct {
		field string
		got   any
		want  any
	}{
		{"TimestampNs", event.TimestampNs, uint64(1_788_000_000_000)},
		{"CgroupID", event.CgroupID, uint64(4026531841)},
		{"PID", event.PID, uint32(4242)},
		{"TGID", event.TGID, uint32(4200)},
		{"UID", event.UID, uint32(1000)},
		{"Comm", event.Comm, "curl"},
		{"Destination", event.Destination.String(), "93.184.216.34"},
		{"Source", event.Source.String(), "10.0.0.5"},
		// A porta é o campo que mais engana, e este projeto já errou nele: ler
		// com a ordem trocada faz 443 virar 47873, número plausível que ninguém
		// questiona.
		{"DestinationPort", event.DestinationPort, uint16(443)},
		{"SourcePort", event.SourcePort, uint16(51234)},
	}
	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.field, testCase.got, testCase.want)
		}
	}
}

// TestDecodeNetHandlesIPv6 cobre o outro ramo de família.
func TestDecodeNetHandlesIPv6(t *testing.T) {
	raw := buildRawNetEvent(familyIPv6, [4]byte{}, [4]byte{}, 443, 51234, "curl")
	// 2606:4700:4700::1111, o resolvedor público da Cloudflare.
	address := netip.MustParseAddr("2606:4700:4700::1111").As16()
	copy(raw[60:60+addressV6Length], address[:])

	event, err := DecodeNet(raw)
	if err != nil {
		t.Fatalf("decodificação falhou: %v", err)
	}
	if event.Destination.String() != "2606:4700:4700::1111" {
		t.Errorf("destino v6: got %q", event.Destination.String())
	}
	if event.DestinationPort != 443 {
		t.Errorf("porta: got %d, want 443", event.DestinationPort)
	}
}

// TestIsPrivateDestination é o teste que justifica a probe existir.
//
// `SECURITY.md:202-205` admite que a ferramenta de shell alcança a rede interna
// e nada a limita. Este campo é o que separa "chamou a API do modelo" de
// "varreu a sub-rede" sem alguém ter de olhar endereço por endereço.
func TestIsPrivateDestination(t *testing.T) {
	cases := []struct {
		name    string
		address [4]byte
		want    bool
	}{
		{"internet pública", [4]byte{93, 184, 216, 34}, false},
		{"rede privada 10/8", [4]byte{10, 0, 0, 5}, true},
		{"rede privada 192.168/16", [4]byte{192, 168, 1, 1}, true},
		{"rede privada 172.16/12", [4]byte{172, 16, 0, 1}, true},
		{"loopback", [4]byte{127, 0, 0, 1}, true},
		// O endereço de metadados da nuvem. É link-local, e é a conexão mais
		// interessante que esta máquina pode produzir: quem o alcança lê as
		// credenciais da instância.
		{"metadados da nuvem", [4]byte{169, 254, 169, 254}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			raw := buildRawNetEvent(familyIPv4, testCase.address, [4]byte{10, 0, 0, 1}, 443, 1234, "x")
			event, err := DecodeNet(raw)
			if err != nil {
				t.Fatalf("decodificação falhou: %v", err)
			}
			if got := event.IsPrivateDestination(); got != testCase.want {
				t.Errorf("%s (%s): got %v, want %v",
					testCase.name, event.Destination, got, testCase.want)
			}
		})
	}
}

// TestDecodeNetRefusesShortEvent prova que registro truncado vira erro.
//
// Decodificar o que veio leria além do vetor, que em Go é pânico — e derrubaria
// o coletor inteiro por causa de um evento malformado.
func TestDecodeNetRefusesShortEvent(t *testing.T) {
	if _, err := DecodeNet(make([]byte, NetEventSize-1)); !errors.Is(err, ErrShortEvent) {
		t.Fatalf("evento curto não foi recusado: %v", err)
	}
}

// TestDecodeNetUnknownFamilyFallsBackToIPv4 cobre o ramo padrão.
//
// Família desconhecida não pode virar pânico nem endereço vazio: o registro sai
// como IPv4, que é o que o payload traz preenchido, e quem investigar vê um
// endereço em vez de um buraco.
func TestDecodeNetUnknownFamilyFallsBackToIPv4(t *testing.T) {
	raw := buildRawNetEvent(999, [4]byte{8, 8, 8, 8}, [4]byte{10, 0, 0, 1}, 53, 1234, "dig")
	event, err := DecodeNet(raw)
	if err != nil {
		t.Fatalf("decodificação falhou: %v", err)
	}
	if event.Destination.String() != "8.8.8.8" {
		t.Errorf("família desconhecida perdeu o endereço: %q", event.Destination.String())
	}
}
