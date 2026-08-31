package decode

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

// buildRawEvent monta um registro com o MESMO layout que o programa em C emite.
//
// Escrito à mão, campo a campo, e não com `binary.Write` de uma struct Go: uma
// struct Go seria o mesmo palpite que se quer verificar, e o teste concordaria
// com o defeito. Aqui os offsets são literais, tirados do arquivo em C.
func buildRawEvent(timestamp, cgroup uint64, pid, tgid, uid, gid uint32, comm, filename string) []byte {
	raw := make([]byte, EventSize)
	binary.LittleEndian.PutUint64(raw[0:8], timestamp)
	binary.LittleEndian.PutUint64(raw[8:16], cgroup)
	binary.LittleEndian.PutUint32(raw[16:20], pid)
	binary.LittleEndian.PutUint32(raw[20:24], tgid)
	binary.LittleEndian.PutUint32(raw[24:28], uid)
	binary.LittleEndian.PutUint32(raw[28:32], gid)
	copy(raw[32:32+commLength], comm)
	copy(raw[48:48+filenameLength], filename)
	return raw
}

// TestEventSizeMatchesContract trava o tamanho do registro.
//
// Se alguém acrescentar um campo no C sem acrescentar aqui, todos os campos
// posteriores passam a ser lidos deslocados — e continuam sendo números
// plausíveis. Este teste é a única coisa entre essa mudança e um registro de
// auditoria que mente.
func TestEventSizeMatchesContract(t *testing.T) {
	const expected = 304
	if EventSize != expected {
		t.Fatalf("EventSize = %d, contrato com exec.bpf.c diz %d", EventSize, expected)
	}
}

// TestDecodeReadsEveryField prova que cada campo sai do offset certo.
//
// Os valores são deliberadamente DIFERENTES entre si: com pid=tgid=uid=gid, uma
// troca de offset entre eles passaria despercebida — que é exatamente o erro
// mais fácil de cometer aqui.
func TestDecodeReadsEveryField(t *testing.T) {
	raw := buildRawEvent(
		1_788_000_000_000, // timestamp monotônico
		4026531841,        // id de cgroup, na faixa que o kernel usa de verdade
		4242,              // pid
		4200,              // tgid, diferente do pid: é thread contra processo
		1000,              // uid do usuário `agent`
		1001,              // gid diferente do uid, de propósito
		"bash",
		"/nix/store/abc-bash-5.3/bin/bash",
	)

	event, err := Decode(raw)
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
		{"GID", event.GID, uint32(1001)},
		{"Comm", event.Comm, "bash"},
		{"Filename", event.Filename, "/nix/store/abc-bash-5.3/bin/bash"},
	}
	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.field, testCase.got, testCase.want)
		}
	}
}

// TestDecodeStopsAtNul prova que o lixo depois do terminador não vaza.
//
// Não é estética: o buffer de tamanho fixo vem de memória do KERNEL, e o que
// está depois do NUL é o que estava ali antes. Deixar isso passar mandaria
// conteúdo arbitrário de memória do kernel para fora da máquina, num registro
// que atravessa a rede.
func TestDecodeStopsAtNul(t *testing.T) {
	raw := buildRawEvent(1, 1, 1, 1, 0, 0, "sh", "/bin/sh")
	// Lixo depois do NUL, como o kernel deixaria.
	copy(raw[32+3:32+commLength], "LIXO-DE-MEMORIA")
	copy(raw[48+8:48+40], "/caminho/que/nao/deveria/aparecer")

	event, err := Decode(raw)
	if err != nil {
		t.Fatalf("decodificação falhou: %v", err)
	}
	if event.Comm != "sh" {
		t.Errorf("Comm trouxe lixo depois do NUL: %q", event.Comm)
	}
	if event.Filename != "/bin/sh" {
		t.Errorf("Filename trouxe lixo depois do NUL: %q", event.Filename)
	}
}

// TestDecodeHandlesFullBuffer cobre o texto que ocupa o buffer inteiro.
//
// Sem NUL nenhum, o kernel truncou. Devolver o vetor inteiro é o certo — um
// laço que dependesse de achar o terminador leria além do fim.
func TestDecodeHandlesFullBuffer(t *testing.T) {
	long := strings.Repeat("a", filenameLength)
	raw := buildRawEvent(1, 1, 1, 1, 0, 0, "x", long)

	event, err := Decode(raw)
	if err != nil {
		t.Fatalf("decodificação falhou: %v", err)
	}
	if len(event.Filename) != filenameLength {
		t.Errorf("buffer cheio: got %d bytes, want %d", len(event.Filename), filenameLength)
	}
}

// TestDecodeRefusesShortEvent prova que registro truncado vira erro.
//
// O ring buffer não deveria entregar registro curto, mas "não deveria" não é
// garantia — e decodificar o que veio leria além do vetor, que em Go é pânico,
// e derrubaria o coletor inteiro por causa de um evento.
func TestDecodeRefusesShortEvent(t *testing.T) {
	if _, err := Decode(make([]byte, EventSize-1)); !errors.Is(err, ErrShortEvent) {
		t.Fatalf("evento curto não foi recusado: %v", err)
	}
	if _, err := Decode(nil); !errors.Is(err, ErrShortEvent) {
		t.Fatalf("evento nulo não foi recusado: %v", err)
	}
}

// TestDecodeAcceptsLongerEvent aceita registro MAIOR que o contrato.
//
// O ring buffer alinha o que entrega em 8 bytes, então um registro pode chegar
// com bytes de sobra no fim. Recusá-lo faria o coletor descartar todo evento
// numa máquina cujo alinhamento diferisse — falha total, por rigor desnecessário.
func TestDecodeAcceptsLongerEvent(t *testing.T) {
	raw := append(buildRawEvent(7, 7, 7, 7, 0, 0, "ls", "/bin/ls"), 0, 0, 0, 0)
	event, err := Decode(raw)
	if err != nil {
		t.Fatalf("registro com sobra foi recusado: %v", err)
	}
	if event.Filename != "/bin/ls" {
		t.Errorf("Filename = %q", event.Filename)
	}
}

// TestWallClockAddsMonotonicToBoot confere a conversão de relógio.
//
// O campo é monotônico desde o boot; sozinho ele não diz a hora do mundo. Um
// registro de auditoria com carimbo de tempo errado é pior que sem carimbo:
// ele parece correlacionável.
func TestWallClockAddsMonotonicToBoot(t *testing.T) {
	bootTime := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	event := ExecEvent{TimestampNs: uint64(90 * time.Second)}

	got := event.WallClock(bootTime)
	want := bootTime.Add(90 * time.Second)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
