package shipper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/probe/internal/decode"
	"github.com/andrebassi/agent-computer/probe/internal/sample"
)

// fixedBootTime dá conversões previsíveis de relógio nos testes.
func fixedBootTime() time.Time {
	return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
}

// TestNewWithoutEndpointReturnsNil cobre o modo de diagnóstico local.
//
// Sem endpoint o coletor ainda roda e imprime os eventos; o remetente nulo é o
// que torna isso possível sem espalhar checagens pelo laço.
func TestNewWithoutEndpointReturnsNil(t *testing.T) {
	if New("", fixedBootTime()) != nil {
		t.Fatal("endpoint vazio deveria devolver nil")
	}
}

// TestNilShipperIsSafe prova que todo método aceita o receptor nulo.
//
// É o caminho REAL do modo de diagnóstico. Um pânico aqui derrubaria o coletor
// justamente quando alguém está depurando por que ele não funciona.
func TestNilShipperIsSafe(t *testing.T) {
	var nilShipper *Shipper
	nilShipper.Add(decode.ExecEvent{Filename: "/bin/true"})
	if got := nilShipper.Pending(); got != 0 {
		t.Errorf("Pending() = %d", got)
	}
	if got := nilShipper.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d", got)
	}
	if err := nilShipper.Flush(context.Background()); err != nil {
		t.Errorf("Flush() = %v", err)
	}
}

// TestFlushSendsJSONLines confere o formato do corpo e os campos.
//
// Uma linha JSON por evento: o backend consome assim, e um array quebraria a
// ingestão de forma que só aparece em produção.
func TestFlushSendsJSONLines(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	sender.Add(decode.ExecEvent{
		TimestampNs: uint64(30 * time.Second),
		CgroupID:    4026531841,
		PID:         4242,
		TGID:        4200,
		UID:         1000,
		GID:         1001,
		Comm:        "bash",
		Filename:    "/nix/store/abc-bash/bin/bash",
	})
	sender.Add(decode.ExecEvent{PID: 7, Comm: "ls", Filename: "/bin/ls"})

	if err := sender.Flush(context.Background()); err != nil {
		t.Fatalf("entrega falhou: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(received), "\n")
	if len(lines) != 2 {
		t.Fatalf("esperava 2 linhas JSON, veio %d:\n%s", len(lines), received)
	}

	var first payload
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("primeira linha não é JSON válido: %v", err)
	}
	cases := []struct {
		field string
		got   any
		want  any
	}{
		{"pid", first.PID, uint32(4242)},
		{"tgid", first.TGID, uint32(4200)},
		{"uid", first.UID, uint32(1000)},
		{"gid", first.GID, uint32(1001)},
		{"cgroup_id", first.CgroupID, uint64(4026531841)},
		{"comm", first.Comm, "bash"},
		{"filename", first.Filename, "/nix/store/abc-bash/bin/bash"},
		// O carimbo é boot + monotônico, não o monotônico cru.
		{"_time", first.Time, fixedBootTime().Add(30 * time.Second).Format(time.RFC3339Nano)},
	}
	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.field, testCase.got, testCase.want)
		}
	}

	// A fila esvaziou: entrega confirmada não pode reenviar o mesmo evento.
	if got := sender.Pending(); got != 0 {
		t.Errorf("após entrega bem-sucedida sobraram %d evento(s)", got)
	}
}

// TestHostileFilenameStaysEscaped é o teste de segurança deste pacote.
//
// O nome do arquivo vem de um `execve` que o MODELO controla: ele pode criar um
// binário com aspas, chaves e quebra de linha no nome. Montado por concatenação
// de string, isso injetaria campos forjados no registro de auditoria — que é
// justamente o registro criado para não confiar nele.
func TestHostileFilenameStaysEscaped(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hostile := `/tmp/x"}` + "\n" + `{"uid":0,"filename":"/bin/forjado`
	sender := New(server.URL, fixedBootTime())
	sender.Add(decode.ExecEvent{UID: 1000, Filename: hostile, Comm: "x"})
	if err := sender.Flush(context.Background()); err != nil {
		t.Fatalf("entrega falhou: %v", err)
	}

	// Uma linha, e só uma: a quebra de linha embutida no nome NÃO pode ter
	// produzido um segundo registro.
	if lines := strings.Split(strings.TrimSpace(received), "\n"); len(lines) != 1 {
		t.Fatalf("o nome hostil virou %d linhas; houve injeção:\n%s", len(lines), received)
	}
	var decoded payload
	if err := json.Unmarshal([]byte(received), &decoded); err != nil {
		t.Fatalf("o corpo deixou de ser JSON válido: %v", err)
	}
	if decoded.UID != 1000 {
		t.Errorf("o uid forjado sobrescreveu o real: got %d, want 1000", decoded.UID)
	}
	if decoded.Filename != hostile {
		t.Errorf("o nome foi alterado na codificação: %q", decoded.Filename)
	}
}

// TestFlushKeepsBatchOnFailure prova que falha de rede não perde evento.
//
// É o caso do Mac fechado, que é o caso COMUM. Um lote descartado na primeira
// recusa faria o coletor perder exatamente a janela em que ninguém estava
// olhando — que é quando a auditoria mais importa.
func TestFlushKeepsBatchOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	sender.Add(decode.ExecEvent{Filename: "/bin/ls"})
	sender.Add(decode.ExecEvent{Filename: "/bin/cat"})

	if err := sender.Flush(context.Background()); err == nil {
		t.Fatal("HTTP 503 não virou erro")
	}
	if got := sender.Pending(); got != 2 {
		t.Errorf("o lote se perdeu na falha: sobraram %d de 2", got)
	}
}

// TestFlushPreservesOrderAfterFailure prova que a sequência não se embaralha.
//
// Ordem é metade da informação num registro de auditoria: "rodou X e depois Y"
// não é o mesmo que "rodou Y e depois X". O lote que falhou tem de voltar ANTES
// do que chegou durante a tentativa.
func TestFlushPreservesOrderAfterFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	sender.Add(decode.ExecEvent{Filename: "/primeiro"})
	_ = sender.Flush(context.Background())
	sender.Add(decode.ExecEvent{Filename: "/segundo"})

	sender.mutex.Lock()
	defer sender.mutex.Unlock()
	if len(sender.pending) != 2 {
		t.Fatalf("esperava 2 pendentes, veio %d", len(sender.pending))
	}
	if sender.pending[0].Filename != "/primeiro" {
		t.Errorf("o lote que falhou não voltou para o início: %q", sender.pending[0].Filename)
	}
}

// TestBufferDropsOldestAndCounts prova a política de descarte E a contagem.
//
// A contagem é o ponto: sem ela, "nada aconteceu" e "descartei o que aconteceu"
// ficam indistinguíveis no registro — e a segunda é exatamente o que um
// adversário quer que pareça a primeira.
func TestBufferDropsOldestAndCounts(t *testing.T) {
	sender := New("http://127.0.0.1:1", fixedBootTime())
	for index := 0; index < maxBufferedEvents+50; index++ {
		sender.Add(decode.ExecEvent{Filename: fmt.Sprintf("/bin/cmd-%d", index)})
	}

	if got := sender.Pending(); got != maxBufferedEvents {
		t.Errorf("o buffer passou do teto: %d de %d", got, maxBufferedEvents)
	}
	if got := sender.Dropped(); got != 50 {
		t.Errorf("descartes contados: got %d, want 50", got)
	}

	// O mais ANTIGO foi descartado, não o mais recente.
	sender.mutex.Lock()
	first := sender.pending[0].Filename
	last := sender.pending[len(sender.pending)-1].Filename
	sender.mutex.Unlock()
	if first != "/bin/cmd-50" {
		t.Errorf("descartou o lado errado da fila: o mais antigo é %q", first)
	}
	if last != fmt.Sprintf("/bin/cmd-%d", maxBufferedEvents+49) {
		t.Errorf("o mais recente se perdeu: %q", last)
	}
}

// TestAddIsSafeUnderConcurrency exercita o laço do ring buffer contra o timer.
//
// Os dois rodam em goroutines diferentes na produção: um alimenta, o outro
// drena. A suíte roda com -race, e é lá que a ausência de trava apareceria.
func TestAddIsSafeUnderConcurrency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	var waitGroup sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for index := 0; index < 200; index++ {
				sender.Add(decode.ExecEvent{Filename: "/bin/true"})
			}
		}()
	}
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		for index := 0; index < 20; index++ {
			_ = sender.Flush(context.Background())
		}
	}()
	waitGroup.Wait()
}

// TestFlushOnEmptyQueueDoesNothing evita bater no backend à toa.
//
// O timer dispara a cada poucos segundos, inclusive numa máquina ociosa. Uma
// requisição vazia a cada intervalo produziria ruído no log do backend e faria
// a ausência de atividade parecer atividade.
func TestFlushOnEmptyQueueDoesNothing(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	if err := sender.Flush(context.Background()); err != nil {
		t.Fatalf("fila vazia devolveu erro: %v", err)
	}
	if calls != 0 {
		t.Errorf("fila vazia gerou %d requisição(ões)", calls)
	}
}

// TestAddHealthCarriesEveryMetric prova que a amostra de saúde chega inteira.
//
// Os dois sinais vão na MESMA fila de propósito: a saúde diz QUE a máquina
// degradou, os execs dizem QUEM causou, e com filas separadas a investigação
// viraria uma junção manual por horário.
func TestAddHealthCarriesEveryMetric(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	sender.AddHealth(sample.Health{
		CPUPressureAvg10:    1.25,
		MemoryPressureAvg10: 7.50,
		IOPressureAvg10:     0.10,
		MemTotalKB:          1000,
		MemAvailableKB:      250,
		SwapFreeKB:          2048,
	})
	if err := sender.Flush(context.Background()); err != nil {
		t.Fatalf("entrega falhou: %v", err)
	}

	var decoded payload
	if err := json.Unmarshal([]byte(strings.TrimSpace(received)), &decoded); err != nil {
		t.Fatalf("corpo não é JSON válido: %v", err)
	}
	cases := []struct {
		field string
		got   any
		want  any
	}{
		{"kind", decoded.Kind, kindHealth},
		{"cpu_pressure", decoded.CPUPressure, 1.25},
		{"memory_pressure", decoded.MemoryPressure, 7.50},
		{"io_pressure", decoded.IOPressure, 0.10},
		// 250 de 1000 disponíveis = 75% em uso. Derivado de MemAvailable, e não
		// de MemFree, que numa máquina saudável é sempre baixo por causa do cache.
		{"memory_used_fraction", decoded.MemoryUsed, 0.75},
		{"mem_available_kb", decoded.MemAvailableKB, uint64(250)},
		{"swap_free_kb", decoded.SwapFreeKB, uint64(2048)},
	}
	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.field, testCase.got, testCase.want)
		}
	}
}

// TestExecPayloadOmitsHealthFields prova que os dois tipos não se misturam.
//
// Sem `omitempty`, todo evento de exec carregaria sete campos de saúde zerados
// — o dobro de bytes na rede e no índice, e cada zero indistinguível de uma
// medição que deu zero de verdade.
func TestExecPayloadOmitsHealthFields(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	sender.Add(decode.ExecEvent{PID: 42, Filename: "/bin/ls", Comm: "ls"})
	if err := sender.Flush(context.Background()); err != nil {
		t.Fatalf("entrega falhou: %v", err)
	}

	for _, field := range []string{"cpu_pressure", "memory_pressure", "io_pressure", "swap_free_kb"} {
		if strings.Contains(received, field) {
			t.Errorf("o evento de exec carrega o campo de saúde %q:\n%s", field, received)
		}
	}
	if !strings.Contains(received, `"kind":"exec"`) {
		t.Errorf("o evento de exec não foi marcado como tal:\n%s", received)
	}
}

// TestAddHealthOnNilShipperIsSafe cobre o modo de diagnóstico local.
func TestAddHealthOnNilShipperIsSafe(t *testing.T) {
	var nilShipper *Shipper
	nilShipper.AddHealth(sample.Health{MemTotalKB: 1})
}

// TestAddHealthRespectsBufferCap prova que a saúde não fura o teto de buffer.
//
// Se ela furasse, uma máquina com o Mac fechado por dias acumularia amostras
// sem limite — e o coletor viraria a causa da degradação que ele mede.
func TestAddHealthRespectsBufferCap(t *testing.T) {
	sender := New("http://127.0.0.1:1", fixedBootTime())
	for index := 0; index < maxBufferedEvents+10; index++ {
		sender.AddHealth(sample.Health{MemTotalKB: 1000, MemAvailableKB: uint64(index)})
	}
	if got := sender.Pending(); got != maxBufferedEvents {
		t.Errorf("o buffer passou do teto: %d", got)
	}
	if got := sender.Dropped(); got != 10 {
		t.Errorf("descartes contados: got %d, want 10", got)
	}
}

// TestAddNetCarriesConnectionFields prova que a conexão chega inteira.
//
// A porta e o endereço são os dois campos que mais enganam nesta cadeia: a
// porta vem em ordem de rede do kernel, e o endereço são quatro bytes crus.
// Errar qualquer um produz número plausível e errado.
func TestAddNetCarriesConnectionFields(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	sender.AddNet(decode.NetEvent{
		PID:             4242,
		UID:             1000,
		Comm:            "curl",
		Destination:     netip.MustParseAddr("93.184.216.34"),
		DestinationPort: 443,
		SourcePort:      51234,
	})
	if err := sender.Flush(context.Background()); err != nil {
		t.Fatalf("entrega falhou: %v", err)
	}

	var decoded payload
	if err := json.Unmarshal([]byte(strings.TrimSpace(received)), &decoded); err != nil {
		t.Fatalf("corpo não é JSON válido: %v", err)
	}
	cases := []struct {
		field string
		got   any
		want  any
	}{
		{"kind", decoded.Kind, kindNetwork},
		{"destination", decoded.Destination, "93.184.216.34"},
		{"destination_port", decoded.DestinationPort, uint16(443)},
		{"source_port", decoded.SourcePort, uint16(51234)},
		{"comm", decoded.Comm, "curl"},
		{"private_destination", decoded.PrivateDestination, false},
	}
	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.field, testCase.got, testCase.want)
		}
	}
}

// TestPrivateDestinationIsAlwaysPresent prova que o campo NÃO é omitido.
//
// É o campo de segurança desta probe, e `omitempty` nele faria "conexão
// pública" e "campo ausente" ficarem idênticos na consulta — transformando a
// resposta mais comum em silêncio.
func TestPrivateDestinationIsAlwaysPresent(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	sender.AddNet(decode.NetEvent{
		Destination:     netip.MustParseAddr("1.1.1.1"),
		DestinationPort: 443,
	})
	if err := sender.Flush(context.Background()); err != nil {
		t.Fatalf("entrega falhou: %v", err)
	}
	if !strings.Contains(received, `"private_destination":false`) {
		t.Errorf("o campo sumiu do JSON quando é falso:\n%s", received)
	}
}

// TestAddNetMarksCloudMetadata é o caso que mais importa.
//
// `169.254.169.254` é o endereço de metadados da nuvem: quem o alcança lê as
// credenciais da instância. É a conexão mais interessante que esta máquina pode
// produzir, e ela precisa aparecer marcada.
func TestAddNetMarksCloudMetadata(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := New(server.URL, fixedBootTime())
	sender.AddNet(decode.NetEvent{
		Comm:            "curl",
		Destination:     netip.MustParseAddr("169.254.169.254"),
		DestinationPort: 80,
	})
	if err := sender.Flush(context.Background()); err != nil {
		t.Fatalf("entrega falhou: %v", err)
	}
	if !strings.Contains(received, `"private_destination":true`) {
		t.Errorf("o endereço de metadados da nuvem NÃO foi marcado:\n%s", received)
	}
}

// TestAddNetOnNilShipperIsSafe cobre o modo de diagnóstico local.
func TestAddNetOnNilShipperIsSafe(t *testing.T) {
	var nilShipper *Shipper
	nilShipper.AddNet(decode.NetEvent{DestinationPort: 443})
}

// TestAddNetRespectsBufferCap prova que a conexão não fura o teto.
//
// É a probe de maior volume potencial: uma varredura de rede produz milhares de
// eventos em segundos, e sem o teto ela derrubaria o coletor por memória —
// exatamente durante o evento que ele existe para registrar.
func TestAddNetRespectsBufferCap(t *testing.T) {
	sender := New("http://127.0.0.1:1", fixedBootTime())
	for index := 0; index < maxBufferedEvents+25; index++ {
		sender.AddNet(decode.NetEvent{DestinationPort: uint16(index % 65535)})
	}
	if got := sender.Pending(); got != maxBufferedEvents {
		t.Errorf("o buffer passou do teto: %d", got)
	}
	if got := sender.Dropped(); got != 25 {
		t.Errorf("descartes contados: got %d, want 25", got)
	}
}

// TestFlushIntervalIsExposed confere o valor que o coletor usa no timer.
func TestFlushIntervalIsExposed(t *testing.T) {
	if FlushInterval() != flushInterval {
		t.Errorf("FlushInterval() = %v, want %v", FlushInterval(), flushInterval)
	}
}
