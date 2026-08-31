// Package sample lê a saúde da máquina de /proc, em intervalo fixo.
//
// NÃO É eBPF, e dizer isso é parte do desenho.
//
// Para a pergunta "a máquina está pior hoje que ontem", eBPF é a ferramenta
// errada: o kernel JÁ calcula exatamente essa métrica em /proc/pressure, e uma
// probe que a recalculasse a partir de eventos seria mais cara e menos precisa.
// Vender eBPF como substituto de PSI seria a mesma inflação que este
// repositório já recusou uma vez, ao medir −82% de memória no KasmVNC e decidir
// não trocar porque nada estava limitando.
//
// A divisão de trabalho é a tese: PSI e /proc dizem QUE degradou e QUANTO; as
// probes de kernel dizem QUEM causou. Uma sem a outra responde metade.
package sample

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Health é uma foto da saúde da máquina.
type Health struct {
	// CPUPressureAvg10 é a fração de tempo, nos últimos 10s, em que ALGUMA
	// tarefa esperou por CPU. É o `some` do PSI.
	//
	// Vem em porcentagem. Zero numa máquina ociosa; passar de 10 sustentado
	// significa que há mais trabalho que processador — nesta máquina, de 2
	// vCPU, é o sintoma de mais telas do que ela comporta.
	CPUPressureAvg10 float64

	// MemoryPressureAvg10 é o mesmo para memória.
	//
	// É o número mais valioso deste pacote. Diferente de "memória livre", ele
	// mede CUSTO: quanto tempo se perdeu esperando por memória. Uma máquina com
	// pouca memória livre e pressão zero está saudável — o kernel só está usando
	// a RAM como cache, que é o que ele deve fazer.
	MemoryPressureAvg10 float64

	// IOPressureAvg10 é o mesmo para entrada e saída de disco.
	IOPressureAvg10 float64

	// MemAvailableKB é o que o kernel estima poder ser dado a um processo novo
	// sem entrar em swap.
	//
	// `MemFree` NÃO serve para isso e é o erro clássico de leitura: numa máquina
	// saudável ele é sempre baixo, porque o kernel usa a RAM ociosa como cache.
	MemAvailableKB uint64

	// MemTotalKB é o total físico, para a fração fazer sentido.
	MemTotalKB uint64

	// SwapFreeKB e SwapTotalKB dizem quanto do swap ainda há.
	//
	// Swap em uso não é problema por si: o kernel move página fria para lá de
	// propósito. Vira problema quando anda junto de pressão de memória alta.
	SwapFreeKB  uint64
	SwapTotalKB uint64
}

// MemoryUsedFraction devolve a fração de memória indisponível, de 0 a 1.
//
// Derivada de `MemAvailable`, não de `MemFree`, pelo motivo do comentário do
// campo. Total zero devolve zero em vez de dividir por zero: um `/proc`
// ilegível não pode derrubar o coletor.
func (h Health) MemoryUsedFraction() float64 {
	if h.MemTotalKB == 0 {
		return 0
	}
	return 1 - float64(h.MemAvailableKB)/float64(h.MemTotalKB)
}

// Read monta a foto lendo os arquivos de /proc a partir de `root`.
//
// A raiz é parâmetro para o teste poder apontar para um diretório com arquivos
// conhecidos. Sem isso, testar exigiria um Linux com pressão de memória
// controlada — que é o mesmo problema que impede testar o carregador BPF, e não
// há razão para herdá-lo aqui.
//
// Arquivo ausente NÃO é erro: `/proc/pressure` só existe com `CONFIG_PSI`
// ligado, e num kernel sem ele o resto da leitura continua útil. Devolver erro
// faria a amostragem inteira parar por causa da parte opcional.
func Read(root string) (Health, error) {
	health := Health{}

	// PSI primeiro, e o erro é ignorado de propósito — ver o comentário acima.
	health.CPUPressureAvg10, _ = readPressure(root + "/pressure/cpu")
	health.MemoryPressureAvg10, _ = readPressure(root + "/pressure/memory")
	health.IOPressureAvg10, _ = readPressure(root + "/pressure/io")

	values, err := readMeminfo(root + "/meminfo")
	if err != nil {
		return health, fmt.Errorf("lendo meminfo: %w", err)
	}
	health.MemTotalKB = values["MemTotal"]
	health.MemAvailableKB = values["MemAvailable"]
	health.SwapTotalKB = values["SwapTotal"]
	health.SwapFreeKB = values["SwapFree"]
	return health, nil
}

// readPressure extrai o `avg10` da linha `some` de um arquivo de PSI.
//
// O formato é:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=0
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
//
// A linha `some` é a que interessa: ela conta o tempo em que PELO MENOS UMA
// tarefa esperou. A `full` conta quando TODAS esperaram, o que numa máquina com
// trabalho variado quase nunca acontece — e um alerta que quase nunca dispara é
// um alerta que ninguém confere.
func readPressure(path string) (float64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "some" {
			continue
		}
		for _, field := range fields[1:] {
			name, value, found := strings.Cut(field, "=")
			if !found || name != "avg10" {
				continue
			}
			return strconv.ParseFloat(value, 64)
		}
	}
	return 0, fmt.Errorf("nenhuma linha 'some' com avg10 em %s", path)
}

// readMeminfo lê os pares nome/valor de /proc/meminfo.
//
// Os valores vêm em kB, com o sufixo na linha: `MemTotal:  4014156 kB`. Só as
// chaves conhecidas são guardadas — o arquivo tem dezenas de linhas, e carregar
// todas para usar quatro seria desperdício num laço que roda a cada intervalo.
func readMeminfo(path string) (map[string]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	wanted := map[string]bool{
		"MemTotal": true, "MemAvailable": true,
		"SwapTotal": true, "SwapFree": true,
	}
	values := make(map[string]uint64, len(wanted))

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, rest, found := strings.Cut(scanner.Text(), ":")
		if !found || !wanted[name] {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		parsed, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			// Linha malformada é pulada, não derruba a leitura: perder um campo
			// é muito menos grave que perder a amostra inteira.
			continue
		}
		values[name] = parsed
	}
	return values, scanner.Err()
}
