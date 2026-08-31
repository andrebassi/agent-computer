package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/andrebassi/agent-computer/agent/internal/adapters/driven/browser"
	"github.com/andrebassi/agent-computer/agent/internal/ports"
)

// BrowserTool expõe uma operação do navegador da tela do agente.
//
// Todas compartilham o mesmo tipo porque a diferença entre elas é só o verbo e
// o esquema — replicar a mecânica de conexão em cinco tipos seria repetir o
// tratamento de erro cinco vezes, e é justamente onde eles divergiriam.
type BrowserTool struct {
	action        string
	screenshotDir string
}

// browserArgs é o formato que o modelo preenche. Cada operação usa um
// subconjunto.
type browserArgs struct {
	URL   string `json:"url"`
	Label string `json:"label"`
	Field string `json:"field"`
	Value string `json:"value"`
}

// NewBrowserTools devolve as cinco operações do navegador.
//
// Elas só são oferecidas ao modelo quando o navegador está de pé; quem monta o
// agente decide isso, e não este pacote.
func NewBrowserTools(screenshotDir string) []ports.Tool {
	actions := []string{"navigate", "read", "links", "click", "fill"}
	out := make([]ports.Tool, 0, len(actions)+1)
	for _, a := range actions {
		out = append(out, &BrowserTool{action: a, screenshotDir: screenshotDir})
	}
	out = append(out, &BrowserTool{action: "screenshot", screenshotDir: screenshotDir})
	return out
}

// Spec descreve a operação para o modelo.
//
// As descrições dizem QUANDO usar, não só o que faz. Um modelo que só sabe o
// que a ferramenta faz tende a chamar a errada — pedir para clicar antes de ler
// a página, por exemplo, e depois adivinhar o rótulo do botão.
func (b *BrowserTool) Spec() ports.ToolSpec {
	switch b.action {
	case "navigate":
		return ports.ToolSpec{
			Name: "browser_navigate",
			Description: "Abre uma URL no navegador da sua tela e devolve o título e o " +
				"endereço FINAL. Confira o endereço devolvido: um redirecionamento para " +
				"página de login significa que você precisa chamar request_takeover.",
			Schema: `{"type":"object","properties":{"url":{"type":"string","description":"endereço a abrir"}},"required":["url"]}`,
		}
	case "read":
		return ports.ToolSpec{
			Name: "browser_read",
			Description: "Lê o texto visível da página atual. Use SEMPRE antes de clicar " +
				"ou preencher, para saber o que existe na tela em vez de adivinhar.",
			Schema: `{"type":"object","properties":{}}`,
		}
	case "links":
		return ports.ToolSpec{
			Name: "browser_links",
			Description: "Lista os links da página com o texto de cada um. Use quando " +
				"precisar navegar e não souber para onde ir.",
			Schema: `{"type":"object","properties":{}}`,
		}
	case "click":
		return ports.ToolSpec{
			Name: "browser_click",
			Description: "Clica no elemento cujo texto visível contenha o rótulo informado. " +
				"O rótulo é o TEXTO que aparece na tela, não um seletor CSS. Leia a página " +
				"antes, para usar um texto que existe.",
			Schema: `{"type":"object","properties":{"label":{"type":"string","description":"texto do botão ou link"}},"required":["label"]}`,
		}
	case "fill":
		return ports.ToolSpec{
			Name: "browser_fill",
			Description: "Preenche um campo de formulário. NUNCA use para senha, código de " +
				"verificação ou qualquer dado sigiloso: campos de senha são recusados, e o " +
				"caminho certo é chamar request_takeover para uma pessoa digitar.",
			Schema: `{"type":"object","properties":{` +
				`"field":{"type":"string","description":"rótulo, nome ou placeholder do campo"},` +
				`"value":{"type":"string","description":"o valor a digitar"}},"required":["field","value"]}`,
		}
	}
	return ports.ToolSpec{
		Name: "browser_screenshot",
		Description: "Captura a tela do navegador num arquivo. Você não verá a imagem; " +
			"ela serve para uma pessoa conferir depois o que estava na tela.",
		Schema: `{"type":"object","properties":{}}`,
	}
}

// Execute conecta ao navegador da tela e roda a operação.
//
// A conexão é aberta e fechada a cada chamada, em vez de mantida. Custa alguns
// milissegundos e evita o problema real: uma conexão de longa duração morre
// quando a aba navega ou o Chrome reinicia, e o erro seguinte apareceria numa
// operação sem relação com a causa.
func (b *BrowserTool) Execute(ctx context.Context, screen int, arguments string) (*ports.ToolResult, error) {
	var args browserArgs
	if arguments != "" && arguments != "{}" {
		if err := decodeArgs(arguments, &args); err != nil {
			return &ports.ToolResult{Output: err.Error(), Failed: true}, nil
		}
	}

	// Validar ANTES de conectar. A ordem inversa gastava uma tentativa de
	// conexão para descobrir algo já sabido, e — pior — devolvia a mensagem
	// errada: uma chamada sem url num momento de navegador caído reclamava do
	// navegador, escondendo o defeito real do argumento.
	switch b.action {
	case "navigate":
		if args.URL == "" {
			return &ports.ToolResult{Output: "informe a url a abrir", Failed: true}, nil
		}
	case "click":
		if args.Label == "" {
			return &ports.ToolResult{Output: "informe o rótulo do que clicar", Failed: true}, nil
		}
	case "fill":
		if args.Field == "" {
			return &ports.ToolResult{Output: "informe o campo a preencher", Failed: true}, nil
		}
	}

	client, err := browser.Connect(ctx, screen)
	if err != nil {
		return &ports.ToolResult{
			Output: fmt.Sprintf("não consegui falar com o navegador: %v", err),
			Failed: true,
		}, nil
	}
	defer func() { _ = client.Close() }()

	var output string
	switch b.action {
	case "navigate":
		output, err = client.Navigate(ctx, args.URL)
	case "read":
		output, err = client.ReadText(ctx)
	case "links":
		output, err = client.ListLinks(ctx, 40)
	case "click":
		output, err = client.Click(ctx, args.Label)
	case "fill":
		output, err = client.Fill(ctx, args.Field, args.Value)
	case "screenshot":
		path := filepath.Join(b.screenshotDir, fmt.Sprintf("screen-%d-%d.png", screen, time.Now().Unix()))
		output, err = client.Screenshot(ctx, path)
		if err == nil {
			output = "captura gravada em " + output
		}
	}

	if err != nil {
		// Erro de navegação vira texto para o modelo se corrigir, e não falha da
		// tarefa: rótulo errado e página que não carregou são recuperáveis.
		return &ports.ToolResult{Output: err.Error(), Failed: true}, nil
	}
	if output == "" {
		output = "(sem conteúdo)"
	}
	return &ports.ToolResult{Output: output}, nil
}
