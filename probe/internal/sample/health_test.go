package sample

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProcTree monta um /proc falso com o conteúdo dado.
//
// Existe para o teste não depender de um Linux com pressão de memória
// controlada — que é o mesmo obstáculo que impede testar o carregador BPF, e
// não há razão para herdá-lo aqui: ler e interpretar texto não precisa de
// kernel nenhum.
func writeProcTree(t *testing.T, meminfo, cpuPressure, memoryPressure, ioPressure string) string {
	t.Helper()
	root := t.TempDir()
	if meminfo != "" {
		if err := os.WriteFile(filepath.Join(root, "meminfo"), []byte(meminfo), 0o644); err != nil {
			t.Fatalf("escrevendo meminfo: %v", err)
		}
	}
	if cpuPressure != "" || memoryPressure != "" || ioPressure != "" {
		pressureDir := filepath.Join(root, "pressure")
		if err := os.MkdirAll(pressureDir, 0o755); err != nil {
			t.Fatalf("criando pressure/: %v", err)
		}
		for name, content := range map[string]string{
			"cpu": cpuPressure, "memory": memoryPressure, "io": ioPressure,
		} {
			if content == "" {
				continue
			}
			if err := os.WriteFile(filepath.Join(pressureDir, name), []byte(content), 0o644); err != nil {
				t.Fatalf("escrevendo pressure/%s: %v", name, err)
			}
		}
	}
	return root
}

// realMeminfo é uma amostra do formato exato do kernel, com as unidades e o
// espaçamento variável que ele produz.
const realMeminfo = `MemTotal:        4014156 kB
MemFree:          245680 kB
MemAvailable:    2996432 kB
Buffers:          102400 kB
Cached:          1856320 kB
SwapCached:            0 kB
SwapTotal:       2097148 kB
SwapFree:        2097148 kB
`

// TestReadParsesEveryField prova que cada campo sai do lugar certo.
//
// Os valores são diferentes entre si de propósito: com números iguais, uma
// troca entre `MemAvailable` e `SwapFree` passaria despercebida — e é a troca
// mais fácil de cometer, porque as duas linhas são vizinhas no arquivo.
func TestReadParsesEveryField(t *testing.T) {
	root := writeProcTree(t, realMeminfo,
		"some avg10=1.25 avg60=0.80 avg300=0.30 total=123456\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"some avg10=7.50 avg60=4.00 avg300=1.10 total=99999\nfull avg10=2.00 avg60=1.00 avg300=0.50 total=42\n",
		"some avg10=0.10 avg60=0.05 avg300=0.01 total=7\n")

	health, err := Read(root)
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}

	cases := []struct {
		field string
		got   any
		want  any
	}{
		{"CPUPressureAvg10", health.CPUPressureAvg10, 1.25},
		{"MemoryPressureAvg10", health.MemoryPressureAvg10, 7.50},
		{"IOPressureAvg10", health.IOPressureAvg10, 0.10},
		{"MemTotalKB", health.MemTotalKB, uint64(4014156)},
		{"MemAvailableKB", health.MemAvailableKB, uint64(2996432)},
		{"SwapTotalKB", health.SwapTotalKB, uint64(2097148)},
		{"SwapFreeKB", health.SwapFreeKB, uint64(2097148)},
	}
	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.field, testCase.got, testCase.want)
		}
	}
}

// TestReadUsesSomeNotFull prova que a linha lida é a certa.
//
// `some` conta quando ALGUMA tarefa esperou; `full` quando TODAS esperaram.
// Numa máquina com trabalho variado a segunda quase nunca dispara, e um alerta
// que quase nunca dispara é um alerta que ninguém confere. Ler a linha errada
// produziria um painel permanentemente verde.
func TestReadUsesSomeNotFull(t *testing.T) {
	root := writeProcTree(t, realMeminfo,
		"some avg10=42.00 avg60=0.00 avg300=0.00 total=1\nfull avg10=99.00 avg60=0.00 avg300=0.00 total=1\n",
		"", "")

	health, err := Read(root)
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if health.CPUPressureAvg10 != 42.00 {
		t.Errorf("leu a linha errada: got %v, want 42.00 (a de 'some')", health.CPUPressureAvg10)
	}
}

// TestReadSurvivesMissingPressure cobre o kernel sem CONFIG_PSI.
//
// `/proc/pressure` é opcional. Fazer a amostragem inteira falhar por causa dele
// tiraria também os dados de memória, que são o que resta de útil — e a máquina
// onde isso aconteceria é justamente uma que ninguém escolheu.
func TestReadSurvivesMissingPressure(t *testing.T) {
	root := writeProcTree(t, realMeminfo, "", "", "")

	health, err := Read(root)
	if err != nil {
		t.Fatalf("a ausência de PSI derrubou a leitura inteira: %v", err)
	}
	if health.MemTotalKB != 4014156 {
		t.Errorf("os dados de memória se perderam junto: MemTotalKB=%d", health.MemTotalKB)
	}
	if health.CPUPressureAvg10 != 0 {
		t.Errorf("sem PSI a pressão deveria ficar em zero, veio %v", health.CPUPressureAvg10)
	}
}

// TestReadFailsWithoutMeminfo prova que a parte OBRIGATÓRIA reprova.
//
// É o outro lado da tolerância acima: se tudo fosse opcional, uma leitura de um
// caminho errado devolveria zeros e o painel mostraria uma máquina
// perfeitamente saudável que não existe.
func TestReadFailsWithoutMeminfo(t *testing.T) {
	if _, err := Read(t.TempDir()); err == nil {
		t.Fatal("a falta de meminfo não virou erro; zeros passariam por saúde")
	}
}

// TestReadSkipsMalformedLine prova que uma linha quebrada não leva as outras.
func TestReadSkipsMalformedLine(t *testing.T) {
	root := writeProcTree(t,
		"MemTotal:        nao-e-numero kB\nMemAvailable:    2996432 kB\n", "", "", "")

	health, err := Read(root)
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if health.MemAvailableKB != 2996432 {
		t.Errorf("a linha boa se perdeu junto com a quebrada: %d", health.MemAvailableKB)
	}
}

// TestMemoryUsedFraction confere a conta e o caso de divisão por zero.
//
// A fração vem de `MemAvailable`, e não de `MemFree`. É a diferença entre medir
// saúde e medir cache: numa máquina saudável o `MemFree` é sempre baixo, porque
// o kernel usa a RAM ociosa como cache — e um painel construído sobre ele
// alarmaria todo dia.
func TestMemoryUsedFraction(t *testing.T) {
	cases := []struct {
		name   string
		health Health
		want   float64
	}{
		{"metade disponível", Health{MemTotalKB: 1000, MemAvailableKB: 500}, 0.5},
		{"tudo disponível", Health{MemTotalKB: 1000, MemAvailableKB: 1000}, 0},
		{"nada disponível", Health{MemTotalKB: 1000, MemAvailableKB: 0}, 1},
		// Total zero é `/proc` ilegível. Devolver zero em vez de dividir por
		// zero: um NaN no painel vira um alerta que ninguém entende.
		{"total zero não divide por zero", Health{}, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.health.MemoryUsedFraction(); got != testCase.want {
				t.Errorf("got %v, want %v", got, testCase.want)
			}
		})
	}
}
