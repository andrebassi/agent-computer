package decode

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// Tamanhos do contrato com net.bpf.c.
const (
	// addressV6Length é o tamanho de um endereço IPv6 em bytes.
	addressV6Length = 16

	// NetEventSize é o tamanho de `struct net_event`.
	//
	// A conta, campo a campo: 8+8 dos dois u64, 4×5 dos cinco u32, 2×4 dos
	// quatro u16, 16 do `comm` e 16 do endereço v6 = 76 bytes de conteúdo.
	//
	// O tamanho é 80, e não 76: a struct contém `__u64`, então seu alinhamento é
	// de 8 bytes, e o compilador arredonda o total para o múltiplo seguinte. Os
	// 4 bytes de sobra ficam no FIM e não deslocam nada — mas errar o tamanho
	// faria o leitor recusar todo evento como curto.
	NetEventSize = 80

	// familyIPv4 e familyIPv6 são os valores de AF_INET e AF_INET6 no uapi.
	//
	// Constantes literais em vez de `syscall.AF_INET`: o valor vem do KERNEL
	// LINUX que gerou o evento, e o pacote `syscall` do Go traz o do sistema
	// onde o teste roda. No Mac isso daria outro número, e o teste passaria a
	// concordar com a plataforma errada.
	familyIPv4 = 2
	familyIPv6 = 10
)

// NetEvent é uma conexão TCP que saiu da máquina.
type NetEvent struct {
	// TimestampNs é o relógio monotônico do kernel, como no ExecEvent.
	TimestampNs uint64

	// CgroupID identifica o cgroup v2 de quem discou.
	CgroupID uint64

	PID  uint32
	TGID uint32
	UID  uint32

	// Comm é o nome curto, truncado em 16 bytes. Auxiliar, nunca a resposta.
	Comm string

	// Source e Destination são os endereços, já interpretados.
	//
	// `netip.Addr` e não string: ele guarda IPv4 e IPv6 no mesmo tipo, compara
	// sem alocar, e — o que importa aqui — sabe responder `IsPrivate()`, que é a
	// pergunta que este evento existe para permitir.
	Source      netip.Addr
	Destination netip.Addr

	// SourcePort e DestinationPort já vêm em ordem de host.
	SourcePort      uint16
	DestinationPort uint16
}

// IsPrivateDestination diz se a conexão foi para a rede interna.
//
// É a pergunta que motivou esta probe. `SECURITY.md:202-205` admite que "a
// ferramenta de shell alcança a rede interna diretamente, e nada aqui a
// limita" — e sem este campo, distinguir "o agente chamou a API do modelo" de
// "o agente varreu a sub-rede" exigiria olhar endereço por endereço.
//
// Cobre também o link-local, que inclui `169.254.169.254` — o endereço de
// metadados da nuvem, e a conexão mais interessante que esta máquina pode
// produzir.
func (e NetEvent) IsPrivateDestination() bool {
	return e.Destination.IsPrivate() ||
		e.Destination.IsLoopback() ||
		e.Destination.IsLinkLocalUnicast()
}

// DecodeNet traduz um registro de conexão do ring buffer.
func DecodeNet(raw []byte) (NetEvent, error) {
	if len(raw) < NetEventSize {
		return NetEvent{}, fmt.Errorf("%w: %d bytes, esperados %d",
			ErrShortEvent, len(raw), NetEventSize)
	}

	family := binary.LittleEndian.Uint16(raw[40:42])
	event := NetEvent{
		TimestampNs: binary.LittleEndian.Uint64(raw[0:8]),
		CgroupID:    binary.LittleEndian.Uint64(raw[8:16]),
		PID:         binary.LittleEndian.Uint32(raw[16:20]),
		TGID:        binary.LittleEndian.Uint32(raw[20:24]),
		UID:         binary.LittleEndian.Uint32(raw[24:28]),
		// As portas vêm em ORDEM DE HOST, já convertidas pelo kernel.
		//
		// 🛑 MEDIDO EM 31/08/2026, e a primeira versão deste código errou: o
		// tracepoint `inet_sock_set_state` faz `ntohs()` antes de preencher o
		// campo — o `print fmt` dele usa `%hu`, que é o indício. Converter de
		// novo aqui INVERTE os bytes, e o sintoma é exatamente o que o
		// comentário anterior previa sem perceber que estava causando: a porta
		// 443 vira 47873 (0x01BB -> 0xBB01) e a 80 vira 20480.
		//
		// O teste unitário NÃO pegou: ele montava o registro com a mesma
		// suposição errada e concordava com o defeito. Quem pegou foi a
		// execução na máquina real, olhando um número que não podia estar certo.
		DestinationPort: binary.LittleEndian.Uint16(raw[36:38]),
		SourcePort:      binary.LittleEndian.Uint16(raw[38:40]),
		Comm:            cString(raw[44 : 44+commLength]),
	}

	switch family {
	case familyIPv6:
		event.Destination = netip.AddrFrom16([16]byte(raw[60 : 60+addressV6Length]))
		// A origem v6 não é copiada pelo programa em C: ela é sempre um
		// endereço local da máquina, e o que interessa é para ONDE a conexão
		// foi. Menos 16 bytes por evento num caminho que pode ter rajada.
	default:
		// IPv4, e também o caso de família desconhecida: os quatro bytes já
		// estão na ordem de rede, que é a mesma que `AddrFrom4` espera.
		event.Destination = netip.AddrFrom4([4]byte(raw[28:32]))
		event.Source = netip.AddrFrom4([4]byte(raw[32:36]))
	}
	return event, nil
}
