package browser

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// settleDelay é a pausa depois de uma ação que muda a página.
//
// Não é gambiarra de espera: `Page.navigate` volta quando o documento começou a
// carregar, não quando terminou, e um clique costuma disparar navegação
// assíncrona. Ler imediatamente devolveria a página anterior — o pior tipo de
// erro, porque o conteúdo é plausível e está errado.
const settleDelay = 2 * time.Second

// Navigate leva a aba para uma URL e devolve o título e o endereço finais.
//
// O endereço final importa: um redirecionamento para página de login é a
// diferença entre "carregou" e "preciso de uma pessoa", e sem devolvê-lo o
// agente concluiria que chegou onde queria.
func (c *Client) Navigate(ctx context.Context, url string) (string, error) {
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}
	if _, err := c.send(ctx, "Page.navigate", map[string]any{"url": url}); err != nil {
		return "", err
	}
	time.Sleep(c.settle)
	return c.describe(ctx)
}

// describe devolve título e URL correntes, em uma linha.
func (c *Client) describe(ctx context.Context) (string, error) {
	out, err := c.evaluate(ctx, `document.title + " | " + location.href`)
	if err != nil {
		return "", err
	}
	return out, nil
}

// ReadText devolve o texto visível da página.
//
// Texto, e não HTML: o HTML de uma página comum tem dezenas de milhares de
// caracteres de marcação que não ajudam o modelo a decidir nada, e cada um deles
// seria cobrado a cada iteração seguinte.
//
// Elementos de script, estilo e os escondidos são descartados — eles aparecem em
// innerText de formas inconsistentes entre navegadores, e conteúdo invisível
// levaria o agente a concluir coisas que a pessoa na tela não vê.
func (c *Client) ReadText(ctx context.Context) (string, error) {
	script := `(() => {
  const drop = new Set(['SCRIPT','STYLE','NOSCRIPT','SVG','IFRAME']);
  const walk = (node, out) => {
    for (const child of node.children || []) {
      if (drop.has(child.tagName)) continue;
      const style = window.getComputedStyle(child);
      if (style.display === 'none' || style.visibility === 'hidden') continue;
      walk(child, out);
    }
  };
  walk(document.body, []);
  return (document.body.innerText || '').replace(/\n{3,}/g, '\n\n').trim();
})()`
	text, err := c.evaluate(ctx, script)
	if err != nil {
		return "", err
	}
	if len(text) > maxTextBytes {
		// Corta pelo fim: numa página, o topo é o conteúdo e o rodapé é
		// navegação e aviso de cookie.
		text = text[:maxTextBytes] + fmt.Sprintf("\n\n[... página truncada em %d caracteres ...]", maxTextBytes)
	}
	return text, nil
}

// ListLinks devolve os links clicáveis com o texto deles.
//
// É o que permite ao agente decidir para onde ir sem adivinhar seletor CSS.
// Links sem texto visível são omitidos: um ícone sem rótulo não dá ao modelo
// como saber o que ele faz.
func (c *Client) ListLinks(ctx context.Context, limit int) (string, error) {
	script := fmt.Sprintf(`(() => {
  const seen = new Set();
  const out = [];
  for (const a of document.querySelectorAll('a[href]')) {
    const label = (a.innerText || '').trim().replace(/\s+/g, ' ');
    if (!label || seen.has(label)) continue;
    const style = window.getComputedStyle(a);
    if (style.display === 'none' || style.visibility === 'hidden') continue;
    seen.add(label);
    out.push(label.slice(0, 60) + '  ->  ' + a.href);
    if (out.length >= %d) break;
  }
  return out.join('\n');
})()`, limit)
	return c.evaluate(ctx, script)
}

// Click clica no primeiro elemento cujo texto visível contenha o rótulo.
//
// Buscar por TEXTO, e não por seletor CSS, é decisão deliberada: o modelo vê a
// página como texto, então pedir a ele um seletor seria pedir que adivinhasse
// uma estrutura que ele não enxerga. O texto é o que ele acabou de ler.
func (c *Client) Click(ctx context.Context, label string) (string, error) {
	escaped := strings.ReplaceAll(label, `"`, `\"`)
	script := fmt.Sprintf(`(() => {
  const alvo = "%s".toLowerCase();
  const clicaveis = document.querySelectorAll('a, button, input[type=submit], input[type=button], [role=button], [onclick]');
  for (const el of clicaveis) {
    const texto = ((el.innerText || el.value || el.getAttribute('aria-label') || '')).trim().toLowerCase();
    if (!texto.includes(alvo)) continue;
    const style = window.getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden') continue;
    el.scrollIntoView({block: 'center'});
    el.click();
    return 'cliquei em: ' + (el.innerText || el.value || '').trim().slice(0, 60);
  }
  return 'NAO_ENCONTRADO';
})()`, escaped)
	result, err := c.evaluate(ctx, script)
	if err != nil {
		return "", err
	}
	if result == "NAO_ENCONTRADO" {
		return "", fmt.Errorf("nenhum elemento clicável com o texto %q; use browser_links para ver o que existe", label)
	}
	time.Sleep(c.settle)
	where, err := c.describe(ctx)
	if err != nil {
		return result, nil
	}
	return result + "\nagora em: " + where, nil
}

// Fill preenche um campo de formulário identificado pelo rótulo, placeholder ou
// nome, e devolve o que foi preenchido.
//
// ⚠️ NÃO deve ser usado para senha. Quem preenche credencial é a pessoa, pela
// tela — é o que a documentação manda, e o motivo é que o valor passaria pelo
// modelo e pelo histórico no caminho até aqui.
func (c *Client) Fill(ctx context.Context, field, value string) (string, error) {
	escapedField := strings.ReplaceAll(field, `"`, `\"`)
	escapedValue := strings.ReplaceAll(value, `"`, `\"`)
	script := fmt.Sprintf(`(() => {
  const alvo = "%s".toLowerCase();
  const campos = document.querySelectorAll('input, textarea, select');
  for (const el of campos) {
    if (el.type === 'password') continue;
    const pistas = [el.name, el.id, el.placeholder, el.getAttribute('aria-label'),
                    (el.labels && el.labels[0] && el.labels[0].innerText) || ''];
    if (!pistas.some(p => (p || '').toLowerCase().includes(alvo))) continue;
    el.focus();
    el.value = "%s";
    // Frameworks só reagem ao evento, não à atribuição direta: sem isto o
    // campo mostra o texto e o formulário continua achando que está vazio.
    el.dispatchEvent(new Event('input', {bubbles: true}));
    el.dispatchEvent(new Event('change', {bubbles: true}));
    return 'preenchi ' + (el.name || el.id || el.placeholder || 'o campo');
  }
  return 'NAO_ENCONTRADO';
})()`, escapedField, escapedValue)
	result, err := c.evaluate(ctx, script)
	if err != nil {
		return "", err
	}
	if result == "NAO_ENCONTRADO" {
		return "", fmt.Errorf("nenhum campo com %q (campos de senha são ignorados de propósito)", field)
	}
	return result, nil
}

// Screenshot captura a tela e grava num arquivo, devolvendo o caminho.
//
// O modelo não enxerga a imagem — ela serve para a PESSOA conferir o que o
// agente estava vendo quando algo deu errado. Por isso o arquivo vai para o
// workspace durável, e não para um temporário.
func (c *Client) Screenshot(ctx context.Context, path string) (string, error) {
	raw, err := c.send(ctx, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return "", err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := jsonUnmarshal(raw, &out); err != nil {
		return "", err
	}
	if err := writeBase64(path, out.Data); err != nil {
		return "", err
	}
	return path, nil
}
