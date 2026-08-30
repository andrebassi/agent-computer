package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/domain"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// barrierTool só termina quando TODAS as irmãs tiverem começado.
//
// É barreira, e não cronômetro: um teste que medisse tempo passaria por acaso
// numa máquina rápida e falharia sob carga. Aqui, execução em série trava na
// barreira e o teste falha por prazo esgotado — deterministicamente.
type barrierTool struct {
	name    string
	arrived *sync.WaitGroup
	release chan struct{}
	peak    *atomic.Int32
	running *atomic.Int32
}

// Spec declara a ferramenta como concorrente.
func (b *barrierTool) Spec() ports.ToolSpec {
	return ports.ToolSpec{Name: b.name, Description: "teste", Concurrent: true}
}

// Execute registra a chegada, espera as irmãs e devolve.
func (b *barrierTool) Execute(_ context.Context, _ int, _ string) (*ports.ToolResult, error) {
	atual := b.running.Add(1)
	for {
		anterior := b.peak.Load()
		if atual <= anterior || b.peak.CompareAndSwap(anterior, atual) {
			break
		}
	}
	b.arrived.Done()
	<-b.release
	b.running.Add(-1)
	return &ports.ToolResult{Output: b.name}, nil
}

// serialTool não é concorrente e registra a concorrência máxima observada.
type serialTool struct {
	name    string
	peak    *atomic.Int32
	running *atomic.Int32
}

// Spec declara a ferramenta como NÃO concorrente.
func (s *serialTool) Spec() ports.ToolSpec {
	return ports.ToolSpec{Name: s.name, Description: "teste"}
}

// Execute mede quantas rodam ao mesmo tempo.
func (s *serialTool) Execute(_ context.Context, _ int, _ string) (*ports.ToolResult, error) {
	atual := s.running.Add(1)
	for {
		anterior := s.peak.Load()
		if atual <= anterior || s.peak.CompareAndSwap(anterior, atual) {
			break
		}
	}
	time.Sleep(time.Millisecond)
	s.running.Add(-1)
	return &ports.ToolResult{Output: s.name}, nil
}

// Ferramentas concorrentes rodam de fato ao mesmo tempo.
//
// A prova é a barreira: em série, a primeira ficaria esperando uma irmã que
// nunca começa, e o teste estouraria o prazo.
func TestConcurrentToolsRunInParallel(t *testing.T) {
	var arrived sync.WaitGroup
	arrived.Add(3)
	release := make(chan struct{})
	var peak, running atomic.Int32

	tools := []ports.Tool{}
	calls := []domain.ToolCall{}
	for _, name := range []string{"api.um", "api.dois", "api.tres"} {
		tools = append(tools, &barrierTool{name: name, arrived: &arrived, release: release,
			peak: &peak, running: &running})
		calls = append(calls, domain.ToolCall{ID: name, Name: name, Arguments: "{}"})
	}
	agent := newAgent(&fakeModel{}, tools, &fakeScreen{}, newFakeStore(), &fakeLock{})

	pronto := make(chan []toolOutcome)
	go func() {
		pronto <- agent.runToolCalls(context.Background(), mustTask(t, "t1", 1), calls)
	}()

	// Se a execução for serial, esta espera nunca termina.
	esperou := make(chan struct{})
	go func() { arrived.Wait(); close(esperou) }()
	select {
	case <-esperou:
	case <-time.After(3 * time.Second):
		t.Fatal("as três não chegaram juntas — a execução foi serial")
	}
	close(release)

	outcomes := <-pronto
	if len(outcomes) != 3 {
		t.Fatalf("esperava 3 resultados, veio %d", len(outcomes))
	}
	if peak.Load() != 3 {
		t.Fatalf("concorrência máxima devia ser 3, foi %d", peak.Load())
	}
}

// Turno MISTO roda inteiro em série.
//
// Basta uma ferramenta com estado no turno para o paralelismo sair: as do
// navegador falam com a mesma aba, e duas ações simultâneas ali não falham —
// fazem a coisa errada, em silêncio.
func TestMixedTurnRunsSerially(t *testing.T) {
	var peak, running atomic.Int32
	tools := []ports.Tool{
		&serialTool{name: "concorrente", peak: &peak, running: &running},
		&serialTool{name: "com-estado", peak: &peak, running: &running},
	}
	// A primeira se declara concorrente; a segunda não.
	tools[0] = &alwaysConcurrentTool{serialTool{name: "concorrente", peak: &peak, running: &running}}

	agent := newAgent(&fakeModel{}, tools, &fakeScreen{}, newFakeStore(), &fakeLock{})
	calls := []domain.ToolCall{
		{ID: "a", Name: "concorrente", Arguments: "{}"},
		{ID: "b", Name: "com-estado", Arguments: "{}"},
	}
	agent.runToolCalls(context.Background(), mustTask(t, "t1", 1), calls)

	if peak.Load() != 1 {
		t.Fatalf("turno misto devia rodar em série; concorrência máxima foi %d", peak.Load())
	}
}

// alwaysConcurrentTool é a serialTool com a declaração invertida.
type alwaysConcurrentTool struct{ serialTool }

// Spec declara concorrência, ao contrário da embutida.
func (a *alwaysConcurrentTool) Spec() ports.ToolSpec {
	return ports.ToolSpec{Name: a.name, Description: "teste", Concurrent: true}
}

// Uma ferramenta só nunca vale o custo de uma goroutine.
func TestSingleCallRunsSerially(t *testing.T) {
	var peak, running atomic.Int32
	tool := &alwaysConcurrentTool{serialTool{name: "api.um", peak: &peak, running: &running}}
	agent := newAgent(&fakeModel{}, []ports.Tool{tool}, &fakeScreen{}, newFakeStore(), &fakeLock{})

	agent.runToolCalls(context.Background(), mustTask(t, "t1", 1),
		[]domain.ToolCall{{ID: "a", Name: "api.um", Arguments: "{}"}})
	if peak.Load() != 1 {
		t.Fatalf("uma chamada só devia rodar direto, concorrência foi %d", peak.Load())
	}
}

// Ferramenta desconhecida faz o turno inteiro rodar em série.
//
// Assim a mensagem de erro sai pelo caminho normal, sem um ramo especial dentro
// do fan-out.
func TestUnknownToolForcesSerialTurn(t *testing.T) {
	var peak, running atomic.Int32
	tool := &alwaysConcurrentTool{serialTool{name: "api.um", peak: &peak, running: &running}}
	agent := newAgent(&fakeModel{}, []ports.Tool{tool}, &fakeScreen{}, newFakeStore(), &fakeLock{})

	outcomes := agent.runToolCalls(context.Background(), mustTask(t, "t1", 1), []domain.ToolCall{
		{ID: "a", Name: "api.um", Arguments: "{}"},
		{ID: "b", Name: "inventada", Arguments: "{}"},
	})
	if peak.Load() != 1 {
		t.Fatalf("turno com ferramenta desconhecida devia ser serial, foi %d", peak.Load())
	}
	if outcomes[1].known {
		t.Fatal("a ferramenta inventada devia vir marcada como desconhecida")
	}
}
