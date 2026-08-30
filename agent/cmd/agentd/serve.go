package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driving/api"
	"github.com/andrebassi/agent-computer/agent/internal/service"
)

// Prazos do servidor HTTP.
//
// O `http.Server` zerado NÃO tem timeout nenhum: uma conexão que abre e nunca
// manda o cabeçalho fica pendurada para sempre, e derrubar o processo assim não
// exigiria nem autenticação.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownGrace     = 30 * time.Second
)

// serve sobe a porta HTTP.
//
// Tudo que exige porta de verdade vive AQUI, e não em internal/: o gate de
// cobertura mede ./internal/... e tem folga estreita, então código que httptest
// não alcança precisa ficar fora do que ele mede.
func serve(ctx context.Context, d *deps, listenAddr, tokenFile string, taskTimeout time.Duration) error {
	// Falha FECHADA: sem token válido, a porta não sobe. Uma porta que abre
	// porque o arquivo sumiu é o pior desfecho possível.
	token, err := api.ReadToken(tokenFile)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	life := service.NewLifecycle(d.store, d.screen, time.Now)

	// RECONCILIAÇÃO ANTES DO LISTENER.
	//
	// Com a porta já aberta, o reconciliador mataria uma tarefa criada há um
	// instante que ainda não tomou a trava — e ela pareceria ter falhado
	// sozinha, sem motivo visível.
	fixed, err := life.Reconcile(ctx, d.lock)
	if err != nil {
		return fmt.Errorf("reconciliando estado do boot: %w", err)
	}
	for _, task := range fixed {
		logger.Warn("estado reconciliado no boot", "tarefa", task.ID, "tela", task.Screen)
	}

	sup := api.NewSupervisor(ctx, d.agentFactory(), d.store, d.screen, d.lock,
		time.Now, taskTimeout, logger)
	server, err := api.NewServer(sup, life, d.store, token, logger)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// SIGTERM: para de aceitar, cancela as tarefas em voo — elas gravam o estado
	// e soltam a trava — e só então sai. Sem isto, o restart deixa tudo em
	// "rodando" no disco e a reconciliação do próximo boot tem trabalho à toa.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("porta aberta", "endereço", listenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	select {
	case err := <-serverErrors:
		return err
	case s := <-signals:
		logger.Info("encerrando", "sinal", s.String())
	case <-ctx.Done():
		logger.Info("encerrando", "motivo", ctx.Err())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	// A ordem importa: primeiro o servidor para de aceitar, depois as tarefas em
	// voo terminam. O inverso deixaria uma requisição nova entrar enquanto as
	// antigas já estão sendo canceladas.
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("o servidor não encerrou limpo", "erro", err)
	}
	return sup.Shutdown(shutdownCtx)
}
