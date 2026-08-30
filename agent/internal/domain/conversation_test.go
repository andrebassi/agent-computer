package domain

import (
	"errors"
	"strings"
	"testing"
)

// A conversa nasce com a instrução de sistema, que carrega as regras de conduta
// do agente.
func TestNewConversationStartsWithSystemPrompt(t *testing.T) {
	c := NewConversation("t1", "você é um agente")
	if len(c.Messages) != 1 || c.Messages[0].Role != RoleSystem {
		t.Fatalf("conversa devia começar com a mensagem de sistema, veio %+v", c.Messages)
	}
}

// Resultado de ferramenta sem o id da chamada faz a API recusar o turno inteiro.
// Barrar aqui troca um erro remoto e obscuro por um erro local e claro.
func TestAddToolResultRequiresCallID(t *testing.T) {
	c := NewConversation("t1", "sistema")
	if err := c.AddToolResult("", "saída"); !errors.Is(err, ErrToolResultWithoutID) {
		t.Fatalf("esperava ErrToolResultWithoutID, veio %v", err)
	}
	if len(c.Messages) != 1 {
		t.Fatal("mensagem inválida não devia entrar no histórico")
	}
}

// O ponto de entrada mais perigoso do histórico: saída de shell e conteúdo de
// página, que é onde um segredo ecoado apareceria.
func TestSecretsAreRemovedFromEveryEntryPoint(t *testing.T) {
	c := NewConversation("t1", "sistema")
	c.TrackSecret("hunter2000")

	c.AddUser("minha senha é hunter2000")
	c.AddAssistant("vou usar hunter2000 agora", nil)
	if err := c.AddToolResult("call-1", "echo hunter2000 -> hunter2000"); err != nil {
		t.Fatalf("AddToolResult falhou: %v", err)
	}

	for i, m := range c.Messages {
		if strings.Contains(m.Content, "hunter2000") {
			t.Fatalf("segredo vazou na mensagem %d (%s): %q", i, m.Role, m.Content)
		}
	}
}

// Segredo curto demais não é rastreado: apagá-lo mutilaria o texto sem proteger
// nada de real.
func TestTrackSecretIgnoresShortValues(t *testing.T) {
	c := NewConversation("t1", "sistema")
	c.TrackSecret("abc")
	c.AddUser("abc aparece aqui")
	if !strings.Contains(c.Messages[1].Content, "abc") {
		t.Fatal("valor curto não devia ser apagado")
	}
}

// Cortar o histórico sem preservar a instrução de sistema remove as regras de
// conduta, e o agente volta a fazer o que foi proibido depois de algumas dezenas
// de turnos.
func TestTrimAlwaysKeepsSystemPrompt(t *testing.T) {
	c := NewConversation("t1", "REGRAS IMPORTANTES")
	for i := 0; i < 30; i++ {
		c.AddUser("mensagem")
	}
	c.Trim(10)
	if len(c.Messages) > 10 {
		t.Fatalf("histórico devia caber em 10, veio %d", len(c.Messages))
	}
	if c.Messages[0].Role != RoleSystem || c.Messages[0].Content != "REGRAS IMPORTANTES" {
		t.Fatalf("instrução de sistema se perdeu no corte: %+v", c.Messages[0])
	}
}

// A API recusa um turno de ferramenta cujo assistant correspondente saiu do
// histórico. O corte precisa avançar até uma mensagem que não seja RoleTool.
func TestTrimNeverLeavesOrphanToolResult(t *testing.T) {
	c := NewConversation("t1", "sistema")
	for i := 0; i < 5; i++ {
		c.AddAssistant("chamando", []ToolCall{{ID: "call", Name: "shell", Arguments: "{}"}})
		if err := c.AddToolResult("call", "saída"); err != nil {
			t.Fatalf("AddToolResult falhou: %v", err)
		}
	}
	c.Trim(4)
	if c.Messages[1].Role == RoleTool {
		t.Fatalf("primeira mensagem após o sistema não pode ser resultado órfão: %+v", c.Messages[1])
	}
}

// Histórico menor que o limite não deve ser tocado, nem limite absurdo deve
// destruir a conversa.
func TestTrimIsNoopBelowLimit(t *testing.T) {
	c := NewConversation("t1", "sistema")
	c.AddUser("uma só")
	antes := len(c.Messages)

	c.Trim(10)
	if len(c.Messages) != antes {
		t.Fatalf("histórico abaixo do limite não devia mudar: %d -> %d", antes, len(c.Messages))
	}
	c.Trim(1)
	if len(c.Messages) != antes {
		t.Fatalf("limite menor que 2 devia ser ignorado: %d -> %d", antes, len(c.Messages))
	}
}

// O resumo serve para diagnóstico e precisa truncar mensagem longa, senão
// imprimir o histórico inteiro polui o log.
func TestSummaryTruncatesAndCountsToolCalls(t *testing.T) {
	c := NewConversation("t1", "sistema")
	c.AddUser(strings.Repeat("x", 200))
	c.AddAssistant("chamando", []ToolCall{
		{ID: "1", Name: "shell", Arguments: "{}"},
		{ID: "2", Name: "browser", Arguments: "{}"},
	})
	s := c.Summary()
	if !strings.Contains(s, "...") {
		t.Fatalf("mensagem longa devia ser truncada: %q", s)
	}
	if !strings.Contains(s, "2 chamada(s)") {
		t.Fatalf("resumo devia contar as chamadas de ferramenta: %q", s)
	}
}
