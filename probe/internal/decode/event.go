// Package decode traduz os bytes crus do ring buffer em estrutura Go.
//
// É a peça mais testável do coletor, e a que concentra os erros que realmente
// acontecem nesta classe de programa: offset trocado, padding esquecido,
// endianness invertida, e o cálculo do texto terminado em NUL. Nenhum desses
// falha alto — todos produzem número plausível e errado.
//
// Fica separada do carregador de propósito: carregar programa BPF exige um
// kernel Linux e privilégio, e não há nem um nem outro no Mac onde este código
// é escrito. A decodificação não exige nada, então é aqui que a corretude é
// provada, com vetor de bytes conhecido.
package decode

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Tamanhos do contrato com o programa em C. Mudar um lado sem o outro produz
// campos deslocados, que é falha silenciosa: os números continuam sendo
// números.
const (
	// commLength é TASK_COMM_LEN do kernel. Não é escolha nossa.
	commLength = 16

	// filenameLength espelha MAX_FILENAME_LEN do exec.bpf.c.
	filenameLength = 256

	// EventSize é o tamanho exato de `struct exec_event`.
	//
	// 8+8 dos dois u64, 4×4 dos quatro u32, mais os dois vetores. Sem padding,
	// porque os campos estão em ordem decrescente de alinhamento — que é por
	// isso que a ordem em C está daquele jeito.
	EventSize = 8 + 8 + 4 + 4 + 4 + 4 + commLength + filenameLength
)

// ErrShortEvent indica um registro menor que o contrato.
//
// É erro, e não um evento parcial: decodificar o que veio produziria campos
// lidos de memória que não existe. Melhor recusar e contar.
var ErrShortEvent = errors.New("evento menor que o tamanho do contrato")

// ExecEvent é um `execve` observado pelo kernel.
type ExecEvent struct {
	// TimestampNs é o relógio MONOTÔNICO do kernel, não a hora do mundo.
	//
	// Serve para ordenar e medir intervalo; não serve para dizer "às 14h32".
	// Converter exige a referência que o coletor guarda na subida — e é por
	// isso que o campo carrega o sufixo, para ninguém tratá-lo como epoch.
	TimestampNs uint64

	// CgroupID identifica o cgroup v2, que é o inode do diretório.
	//
	// É o que distingue o que o `agentd` disparou do que o Chrome disparou —
	// distinção que o uid NÃO faz nesta máquina, porque os dois rodam como
	// `agent`. Muda a cada restart do serviço, então é chave de correlação
	// dentro de uma janela, nunca identidade durável.
	CgroupID uint64

	// PID é a thread; TGID é o processo. Iguais na maioria dos casos.
	PID  uint32
	TGID uint32

	// UID e GID de quem executou. Zero é root.
	UID uint32
	GID uint32

	// Comm é o nome curto do processo, truncado em 16 bytes pelo kernel.
	//
	// Campo AUXILIAR, nunca a resposta: `/usr/local/bin/agentd` chega aqui como
	// `agentd`. Quem quer saber o que rodou lê Filename.
	Comm string

	// Filename é o caminho do binário, como o kernel o registrou.
	Filename string
}

// Decode traduz um registro do ring buffer.
//
// `binary.LittleEndian` explícito em vez de `binary.NativeEndian`: o objeto BPF
// é compilado num Mac ARM e roda num x86_64, e as duas são little-endian — mas
// escrever "nativo" faria a correção depender de onde o Go foi compilado, que é
// uma dependência invisível. Little-endian é o que o programa em C emite.
func Decode(raw []byte) (ExecEvent, error) {
	if len(raw) < EventSize {
		return ExecEvent{}, fmt.Errorf("%w: %d bytes, esperados %d",
			ErrShortEvent, len(raw), EventSize)
	}

	event := ExecEvent{
		TimestampNs: binary.LittleEndian.Uint64(raw[0:8]),
		CgroupID:    binary.LittleEndian.Uint64(raw[8:16]),
		PID:         binary.LittleEndian.Uint32(raw[16:20]),
		TGID:        binary.LittleEndian.Uint32(raw[20:24]),
		UID:         binary.LittleEndian.Uint32(raw[24:28]),
		GID:         binary.LittleEndian.Uint32(raw[28:32]),
		Comm:        cString(raw[32 : 32+commLength]),
		Filename:    cString(raw[32+commLength : 32+commLength+filenameLength]),
	}
	return event, nil
}

// cString corta um vetor de bytes no primeiro NUL.
//
// O kernel escreve texto terminado em NUL num buffer de tamanho fixo e deixa o
// resto como estava. Converter o vetor inteiro para string traria o lixo junto
// — e o lixo é conteúdo de memória do kernel, que não pode sair da máquina.
//
// Sem NUL nenhum, devolve o vetor inteiro: é o caso do texto que ocupou o
// buffer exato, e truncar ali é o comportamento do próprio kernel.
func cString(raw []byte) string {
	for index, value := range raw {
		if value == 0 {
			return string(raw[:index])
		}
	}
	return string(raw)
}

// WallClock converte o relógio monotônico de uma conexão em hora do mundo.
//
// Mesma conversão do ExecEvent, e as duas com a mesma ressalva: a estimativa é
// aproximada porque os relógios andam em ritmos ligeiramente diferentes.
func (e NetEvent) WallClock(bootTime time.Time) time.Time {
	return bootTime.Add(time.Duration(e.TimestampNs))
}

// WallClock converte o relógio monotônico do kernel em hora do mundo.
//
// `bootTime` é o instante que o coletor calculou na subida, comparando o
// relógio do sistema com o monotônico. A conversão é aproximada por
// construção: os dois relógios andam em ritmos ligeiramente diferentes, e a
// deriva cresce com o tempo de vida do processo.
//
// Isso é aceito porque a precisão que importa aqui é de segundos — juntar um
// exec com o trecho da ferramenta que o disparou —, não de microssegundos.
func (e ExecEvent) WallClock(bootTime time.Time) time.Time {
	return bootTime.Add(time.Duration(e.TimestampNs))
}
