package domain

import (
	"errors"
	"fmt"
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
	before := len(c.Messages)

	c.Trim(10)
	if len(c.Messages) != before {
		t.Fatalf("histórico abaixo do limite não devia mudar: %d -> %d", before, len(c.Messages))
	}
	c.Trim(1)
	if len(c.Messages) != before {
		t.Fatalf("limite menor que 2 devia ser ignorado: %d -> %d", before, len(c.Messages))
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

// Comprimir corta a metade antiga e PRESERVA a instrução de sistema.
//
// Cortar a instrução removeria as regras de conduta do agente, e o efeito
// prático seria ele voltar a fazer o que foi proibido — justamente quando o
// histórico ficou longo e a supervisão humana está mais distante.
func TestCompactKeepsSystemAndRecentMessages(t *testing.T) {
	conv := NewConversation("t1", "regras invioláveis")
	for i := 0; i < 10; i++ {
		conv.AddUser(fmt.Sprintf("pedido %d", i))
	}
	before := len(conv.Messages)

	if !conv.Compact() {
		t.Fatal("devia ter comprimido")
	}
	if len(conv.Messages) >= before {
		t.Fatalf("não encurtou: %d -> %d", before, len(conv.Messages))
	}
	if conv.Messages[0].Role != RoleSystem || conv.Messages[0].Content != "regras invioláveis" {
		t.Fatalf("a instrução de sistema devia ser preservada: %+v", conv.Messages[0])
	}
	// O marcador é mensagem de usuário, e não parte da instrução: anexá-lo ao
	// sistema faria a instrução crescer a cada compressão.
	if conv.Messages[1].Role != RoleUser || !strings.Contains(conv.Messages[1].Content, "comprimido") {
		t.Fatalf("devia haver um marcador de usuário: %+v", conv.Messages[1])
	}
	// As mensagens RECENTES são as que ficam — é o trabalho em curso.
	last := conv.Messages[len(conv.Messages)-1]
	if last.Content != "pedido 9" {
		t.Fatalf("a última mensagem devia sobreviver, veio %q", last.Content)
	}
}

// Comprimir NÃO pode deixar resultado de ferramenta órfão.
//
// A API recusa um turno RoleTool cujo assistant correspondente saiu do
// histórico — a mesma regra do Trim, e por isso a varredura é compartilhada.
func TestCompactNeverLeavesOrphanToolResult(t *testing.T) {
	conv := NewConversation("t1", "regras")
	for i := 0; i < 6; i++ {
		conv.AddUser(fmt.Sprintf("pedido %d", i))
		conv.AddAssistant("", []ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "shell", Arguments: "{}"}})
		if err := conv.AddToolResult(fmt.Sprintf("c%d", i), "saída"); err != nil {
			t.Fatalf("preparação falhou: %v", err)
		}
	}
	if !conv.Compact() {
		t.Fatal("devia ter comprimido")
	}
	// Depois do marcador, a primeira mensagem não pode ser resultado de
	// ferramenta: ela não teria a chamada correspondente.
	if conv.Messages[2].Role == RoleTool {
		t.Fatalf("resultado de ferramenta ficou órfão: %+v", conv.Messages[2])
	}
}

// Sem o que cortar, devolve false.
//
// Se devolvesse true, quem chamou refaria a chamada com o mesmo histórico,
// receberia o mesmo erro, e "comprime e tenta de novo" viraria laço infinito.
func TestCompactRefusesWhenAlreadyMinimal(t *testing.T) {
	cases := []struct {
		name  string
		monta func() *Conversation
	}{
		{"só sistema", func() *Conversation { return NewConversation("t1", "regras") }},
		{"sistema e um pedido", func() *Conversation {
			c := NewConversation("t1", "regras")
			c.AddUser("faça algo")
			return c
		}},
		{"sistema e dois turnos", func() *Conversation {
			c := NewConversation("t1", "regras")
			c.AddUser("faça algo")
			c.AddAssistant("ok", nil)
			return c
		}},
	}
	for _, caso := range cases {
		t.Run(caso.name, func(t *testing.T) {
			conv := caso.monta()
			before := len(conv.Messages)
			if conv.Compact() {
				t.Fatal("não devia comprimir uma conversa mínima")
			}
			if len(conv.Messages) != before {
				t.Fatalf("mexeu no histórico mesmo recusando: %d -> %d", before, len(conv.Messages))
			}
		})
	}
}

// Histórico que é só resultado de ferramenta depois do corte também recusa.
//
// A varredura anti-órfão pode consumir tudo que sobrava; devolver true aí
// entregaria uma conversa sem nada além do marcador.
func TestCompactRefusesWhenScanConsumesEverything(t *testing.T) {
	conv := NewConversation("t1", "regras")
	conv.AddUser("faça algo")
	conv.AddAssistant("", []ToolCall{{ID: "c1", Name: "shell", Arguments: "{}"}})
	for i := 0; i < 6; i++ {
		if err := conv.AddToolResult("c1", "saída"); err != nil {
			t.Fatalf("preparação falhou: %v", err)
		}
	}
	// Todas as mensagens da metade final são RoleTool: a varredura vai até o fim.
	if conv.Compact() {
		t.Fatal("devia recusar quando a varredura consome o histórico inteiro")
	}
}

// A varredura anti-órfão é testada direto porque tem uma proteção que nenhum
// chamador atual alcança.
//
// Trim e Compact sempre passam índice positivo — a aritmética dos dois garante
// isso. A proteção existe para que um chamador futuro erre com um índice ruim em
// vez de causar pânico de acesso fora do slice, e testá-la aqui é o que impede
// que ela seja removida por parecer código morto.
func TestSkipOrphanToolResults(t *testing.T) {
	messages := []Message{
		{Role: RoleSystem}, {Role: RoleUser}, {Role: RoleAssistant},
		{Role: RoleTool}, {Role: RoleTool}, {Role: RoleUser},
	}
	cases := []struct {
		name string
		from int
		want int
	}{
		{"já aponta para não-ferramenta", 1, 1},
		{"pula dois resultados seguidos", 3, 5},
		{"índice negativo é tratado como zero", -3, 0},
		{"além do fim devolve o fim", 99, 99},
	}
	for _, caso := range cases {
		t.Run(caso.name, func(t *testing.T) {
			if got := skipOrphanToolResults(messages, caso.from); got != caso.want {
				t.Fatalf("de %d esperava %d, veio %d", caso.from, caso.want, got)
			}
		})
	}
}

// A resposta da tarefa é a última fala do assistente COM conteúdo.
//
// Turnos em que o modelo só chamou ferramenta têm conteúdo vazio e são pulados:
// eles são o COMO, e quem pergunta "o que deu?" quer o quê.
func TestLastAnswerSkipsToolOnlyTurns(t *testing.T) {
	conv := NewConversation("t1", "regras")
	conv.AddUser("conte os núcleos")
	conv.AddAssistant("vou olhar", nil)
	conv.AddAssistant("", []ToolCall{{ID: "c1", Name: "shell", Arguments: "{}"}})
	if err := conv.AddToolResult("c1", "4"); err != nil {
		t.Fatalf("preparação falhou: %v", err)
	}
	conv.AddAssistant("são 4 núcleos", nil)

	if got := conv.LastAnswer(); got != "são 4 núcleos" {
		t.Fatalf("esperava a última fala com conteúdo, veio %q", got)
	}
}

// Sem resposta, devolve vazio — e vazio significa vazio.
//
// Nenhum texto de preenchimento entra aqui: um "tarefa concluída" inventado
// apareceria no aviso de quem recebe como se o agente tivesse dito algo.
func TestLastAnswerIsEmptyWhenNothingWasSaid(t *testing.T) {
	cases := []struct {
		name  string
		monta func() *Conversation
	}{
		{"conversa nova", func() *Conversation { return NewConversation("t1", "regras") }},
		{"só chamada de ferramenta", func() *Conversation {
			c := NewConversation("t1", "regras")
			c.AddUser("faça")
			c.AddAssistant("", []ToolCall{{ID: "c1", Name: "shell", Arguments: "{}"}})
			return c
		}},
	}
	for _, caso := range cases {
		t.Run(caso.name, func(t *testing.T) {
			if got := caso.monta().LastAnswer(); got != "" {
				t.Fatalf("esperava vazio, veio %q", got)
			}
		})
	}
}

// Nota de sistema registra o que aconteceu FORA da conversa.
//
// Entra como sistema porque não é fala de pessoa nem do modelo — confundir os
// três papéis faria o modelo responder a uma nota de infraestrutura como se
// fosse pedido.
func TestAddSystemNote(t *testing.T) {
	conv := NewConversation("t1", "regras")
	if err := conv.AddSystemNote("aviso não entregue: sem rede"); err != nil {
		t.Fatalf("AddSystemNote falhou: %v", err)
	}
	last := conv.Messages[len(conv.Messages)-1]
	if last.Role != RoleSystem || !strings.Contains(last.Content, "sem rede") {
		t.Fatalf("nota errada: %+v", last)
	}
	if err := conv.AddSystemNote(""); err == nil {
		t.Fatal("nota vazia devia ser recusada")
	}
}

// A nota passa pela limpeza de segredos, como qualquer outro conteúdo.
//
// Uma mensagem de erro pode carregar a credencial que causou a falha, e ela iria
// para o histórico — que é gravado em disco e relido a cada iteração.
func TestSystemNoteIsRedacted(t *testing.T) {
	conv := NewConversation("t1", "regras")
	conv.TrackSecret("senha-secreta")
	if err := conv.AddSystemNote("falhou ao autenticar com senha-secreta"); err != nil {
		t.Fatalf("AddSystemNote falhou: %v", err)
	}
	last := conv.Messages[len(conv.Messages)-1]
	if strings.Contains(last.Content, "senha-secreta") {
		t.Fatalf("o segredo vazou para o histórico: %q", last.Content)
	}
}
