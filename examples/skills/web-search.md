# Buscar qualquer coisa na internet

Você está num servidor em nuvem, e isso muda tudo: buscador grande **bloqueia
IP de datacenter**. Medido daqui em 30/08/2026, não suposto.

| Fonte | O que devolve daqui |
|---|---|
| Google | `200` com página de bloqueio — parece sucesso |
| DuckDuckGo | `202`, que é 2xx e é o desafio anti-bot |
| Startpage | `200` sem resultado |
| Brave | `429` |
| **Bing** | ✅ funciona |
| **Mojeek** | ✅ funciona |

Por isso a ordem abaixo. Siga-a: ela vai do mais rápido e certo para o mais
lento e geral.

## 1. Que dia é hoje? Descubra antes, não depois

"nesse domingo", "hoje", "amanhã", "agora" não significam nada sem isso, e você
não tem relógio confiável:

```
date '+%A, %d/%m/%Y %H:%M %Z'
```

Sempre que a pergunta tiver palavra de tempo, este é o **primeiro** comando.

## 2. Pergunta comum: vá direto à fonte, sem buscar

Um `curl` de menos de 1 s vale mais que uma busca. Todas abaixo respondem daqui,
sem autenticação, e foram medidas:

**Dólar em reais** — oficial do Banco Central, e a data importa (só dia útil):
```
curl -sS --max-time 8 "https://api.frankfurter.app/latest?from=USD&to=BRL"
```

**Bitcoin**:
```
curl -sS --max-time 8 "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd,brl"
```

**Temperatura de uma cidade** — troque as coordenadas:
```
curl -sS --max-time 8 "https://api.open-meteo.com/v1/forecast?latitude=-22.91&longitude=-43.17&current=temperature_2m&timezone=America/Sao_Paulo"
```
Rio de Janeiro `-22.91,-43.17` · São Paulo `-23.55,-46.63` · Brasília `-15.79,-47.88`

Alternativa em texto, que já traz a cidade por nome:
```
curl -sS --max-time 8 "https://wttr.in/Rio+de+Janeiro?format=%l:+%t+%C"
```

**Qualquer moeda**: `https://api.frankfurter.app/latest?from=EUR&to=BRL`

⚠️ **A AwesomeAPI devolve `429` daqui.** Não insista nela.

## 3. Pergunta geral: delegue a busca

Para tudo que não está acima — quem joga hoje, o que aconteceu, quem é fulano,
qual a notícia — use `delegate_to_code`. O agente de código tem busca web que
roda **do servidor da Anthropic**, e por isso não passa por nenhum dos bloqueios
da tabela acima.

Peça a **resposta**, não a pesquisa, e sempre exija a fonte:

```
delegate_to_code("Busque na web e responda: quais jogos do Brasileirão Série A
acontecem hoje, 30/08/2026, com horário de Brasília e onde passa. Cite a fonte
e a data de publicação.")
```

Inclua a data que você descobriu no passo 1 — o outro agente **não vê esta
conversa** e não sabe o que "hoje" significa para você.

## 4. Só então o buscador na mão

Se a delegação falhar, o Bing responde daqui:

```
curl -sS --max-time 8 -A "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36" \
  "https://www.bing.com/search?format=rss&q=SUA+BUSCA&setlang=pt-BR" \
  | grep -oE '<(title|description)>[^<]*' | head -12
```

`format=rss` é o detalhe que importa: devolve XML limpo em vez de 124 KB de HTML
com JavaScript. Mojeek é a reserva: `https://www.mojeek.com/search?q=`

E quando o conteúdo só existe depois do JavaScript — portal de notícias é o caso
clássico, o Globo Esporte devolve 858 KB de casca vazia — aí é `browser_navigate`
seguido de `browser_read`, não `curl`.

## 5. Responda com fonte e hora, sempre

Dado volátil sem carimbo de tempo é dado errado esperando a vez:

> Dólar: **R$ 5,16** (Frankfurter/BCE, cotação de 28/08/2026 — não há cotação em
> fim de semana)

Nunca invente o número que faltou. Se nenhuma fonte respondeu, diga qual você
tentou e o que ela devolveu. Um "não consegui, o Bing deu 429" é útil; um valor
inventado destrói a confiança em todos os outros.

## 6. Barreira sensível continua sendo take-over

Se um site pedir login, CAPTCHA ou 2FA, chame `request_takeover` e **pare**. Não
existe pergunta cuja resposta justifique contornar isso.
