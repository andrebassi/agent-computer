// Package lock implementa a trava de uma tarefa por tela.
package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// FileLock usa trava de arquivo do sistema operacional.
//
// Precisa ser trava de sistema, e não um registro em arquivo: dois processos
// que leem "livre" e escrevem "ocupado" podem fazê-lo no mesmo instante e ambos
// seguirem adiante. Com flock, o núcleo garante que só um passa — e a trava é
// liberada sozinha se o processo morrer, o que um arquivo de estado não faria,
// deixando a tela travada para sempre depois de uma queda.
type FileLock struct {
	dir string
}

// NewFileLock cria o diretório das travas.
func NewFileLock(dir string) (*FileLock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("criando diretório de travas: %w", err)
	}
	return &FileLock{dir: dir}, nil
}

// Acquire tenta tomar a tela sem esperar.
//
// A tentativa é não bloqueante de propósito: se a tela está ocupada, quem
// chamou precisa saber agora para avisar a pessoa, e não ficar pendurado sem
// explicação até a outra tarefa terminar.
func (l *FileLock) Acquire(_ context.Context, screen int, taskID string) (func() error, error) {
	path := filepath.Join(l.dir, fmt.Sprintf("screen-%d.lock", screen))
	// 0660, e nao 0644: o GRUPO precisa escrever.
	//
	// Trava de tela e tomada por dois usuarios legitimos -- o servico, que roda
	// como `agentd`, e o CLI do operador, que roda como `agent`. `flock` exige
	// abertura para ESCRITA, entao 0644 deixava o segundo de fora.
	//
	// Medido em 30/08/2026, depois da separacao de usuarios: o teste de busca
	// criava a tarefa, aplicava a habilidade, e morria em "abrindo arquivo de
	// trava: permission denied" -- num diretorio com a permissao certa e um
	// arquivo com o dono certo.
	//
	// Nao e afrouxamento: o conteudo da trava e o nome de quem a segura, nao um
	// segredo, e o diretorio (2750 agentd:agent) ja limita quem chega ate aqui.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o660)
	if err != nil {
		return nil, fmt.Errorf("abrindo arquivo de trava: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		owner, _ := os.ReadFile(path)
		_ = f.Close()
		return nil, fmt.Errorf("%w (tela %d, ocupada por %q)", domain.ErrScreenBusy, screen, string(owner))
	}

	// Grava quem tomou a trava. É diagnóstico, não controle: quem manda é o
	// flock. Serve para a mensagem de erro dizer qual tarefa está segurando.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(taskID), 0)
	}

	release := func() error {
		// A ordem importa: soltar a trava antes de fechar. Fechar já libera,
		// mas ser explícito torna o comportamento independente do sistema.
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}
	return release, nil
}
