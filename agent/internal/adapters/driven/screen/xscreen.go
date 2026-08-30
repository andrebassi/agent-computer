// Package screen controla a tela X de um agente: mostra status e sinaliza
// quando a tarefa espera uma pessoa.
package screen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
)

// commandTimeout é curto porque toda chamada aqui é um utilitário X local que
// responde em milissegundos. Se travar, é sinal de tela morta, e esperar não
// ajuda.
const commandTimeout = 5 * time.Second

// XScreen fala com o servidor X pelos utilitários de linha de comando.
//
// Usar `xsetroot` e `xmessage` em vez de uma biblioteca X é decisão de custo:
// os dois já vêm com o ambiente que o computador instala, e o status não
// precisa de nada mais sofisticado que texto.
type XScreen struct {
	// statusDir guarda a última linha de status de cada tela, para quem
	// consultar de fora do X (o `agent-status`, por exemplo).
	statusDir string
}

// NewXScreen cria o driver e garante o diretório de status.
func NewXScreen(statusDir string) (*XScreen, error) {
	if err := os.MkdirAll(statusDir, 0o755); err != nil {
		return nil, fmt.Errorf("criando diretório de status: %w", err)
	}
	return &XScreen{statusDir: statusDir}, nil
}

// ShowStatus registra a linha de status da tela.
//
// Escreve nos dois lugares de propósito: no arquivo, que é o que ferramentas de
// fora leem e o que sobrevive à queda do X; e no nome da raiz do X, que alguns
// gerenciadores de janela exibem. Depender só do X deixaria o status invisível
// para quem consulta por SSH.
func (x *XScreen) ShowStatus(ctx context.Context, screen int, line string) error {
	path := filepath.Join(x.statusDir, fmt.Sprintf("screen-%d.status", screen))
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		return fmt.Errorf("gravando status: %w", err)
	}
	// Falha no X não é erro fatal: a tarefa continua, e o status persiste no
	// arquivo. Uma tela caída não deve derrubar o trabalho junto.
	_ = x.runX(ctx, screen, "xsetroot", "-name", line)
	return nil
}

// RequestTakeover destaca na tela que a tarefa espera uma pessoa.
//
// A janela é aberta sem esperar retorno: `xmessage` só sai quando alguém fecha,
// e bloquear aqui prenderia o agente num utilitário gráfico.
func (x *XScreen) RequestTakeover(ctx context.Context, screen int, reason domain.BlockReason, detail string) error {
	banner := fmt.Sprintf("PRECISA DE VOCE\n\n%s\n\n%s\n\nResolva o passo e rode:  agentd -resume", reason.Description(), detail)

	// Fecha um pedido anterior que tenha ficado aberto, senão as janelas se
	// empilham a cada bloqueio e escondem a tela do navegador.
	_ = x.ClearTakeover(ctx, screen)

	cmd := exec.Command("xmessage", "-center", "-geometry", "600x220", "-bg", "#7f1d1d", "-fg", "white", banner)
	cmd.Env = append(os.Environ(), fmt.Sprintf("DISPLAY=:%d", screen))
	if err := cmd.Start(); err != nil {
		// Sem janela, o status em arquivo e o nome da raiz ainda avisam.
		return fmt.Errorf("abrindo aviso na tela %d: %w", screen, err)
	}
	// O processo é deliberadamente abandonado: ele vive até ClearTakeover
	// matá-lo. Sem este Release, o processo viraria zumbi ao terminarmos.
	return cmd.Process.Release()
}

// ClearTakeover fecha o aviso quando a pessoa devolve o controle.
func (x *XScreen) ClearTakeover(ctx context.Context, screen int) error {
	// pkill devolve 1 quando não há nada para matar, e isso é sucesso aqui.
	_ = x.runX(ctx, screen, "pkill", "-f", fmt.Sprintf("xmessage.*PRECISA DE VOCE"))
	return nil
}

// runX executa um utilitário apontando para o display da tela.
func (x *XScreen) runX(ctx context.Context, screen int, name string, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("DISPLAY=:%d", screen))
	return cmd.Run()
}
