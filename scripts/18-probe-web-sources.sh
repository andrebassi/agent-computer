#!/bin/bash
# Sonda, DO DROPLET, quais fontes da web respondem a um IP de datacenter.
#
# A habilidade de busca precisa nascer do que funciona ali, e nao do que
# funciona no Mac de casa: buscador grande bloqueia faixa de nuvem, e uma
# habilidade que mande o agente ao Google produziria uma tarefa que falha
# sempre, do mesmo jeito, sem que ninguem entenda por que.
#
# Mede tres coisas por fonte: codigo HTTP, tamanho da resposta e se o conteudo
# esperado esta la. Codigo 200 nao basta -- pagina de bloqueio tambem devolve
# 200, e e por isso que a terceira coluna existe.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
# Sem isto, `droplet_ip` nao consulta a API e TODA sonda reporta falha da fonte
# quando o problema e local -- 16 linhas de "SEM o conteudo esperado" para um
# droplet que estava de pe.
load_token

echo "sondando fontes da web a partir do droplet"
echo "rota: $(agent_route)"
echo

# probe <rotulo> <url> <regex-que-prova-conteudo>
probe() {
  local label="$1" url="$2" proof="$3"
  local out
  out="$(agent_ssh "curl -sS -L --max-time 20 -w '\nHTTPCODE:%{http_code} BYTES:%{size_download}' \
    -A 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36' \
    '$url' 2>&1" || echo "FALHOU")"

  local code bytes
  code="$(echo "$out" | grep -o 'HTTPCODE:[0-9]*' | cut -d: -f2)"
  bytes="$(echo "$out" | grep -o 'BYTES:[0-9]*' | cut -d: -f2)"

  # O veredicto exige as DUAS coisas: codigo bom E conteudo. Medido nesta
  # sonda, cada uma sozinha mente numa direcao:
  #
  #  - o Google devolve 200 com 92 KB de pagina de bloqueio -- codigo sozinho
  #    diria que funcionou;
  #  - o Brave devolve 429 numa pagina cujo texto casa com "result" -- conteudo
  #    sozinho diria que funcionou;
  #  - o DuckDuckGo devolve 202, que e 2xx e parece sucesso, mas e o desafio
  #    anti-bot dele.
  #
  # Por isso o unico codigo aceito e 200, e nao a faixa 2xx.
  local temConteudo="nao"
  echo "$out" | grep -qiE "$proof" && temConteudo="sim"

  if [ "$code" = "200" ] && [ "$temConteudo" = "sim" ]; then
    printf '  %-28s HTTP %-4s %8s B  ✅ serve\n' "$label" "${code:-?}" "${bytes:-0}"
  elif [ "$code" = "200" ]; then
    printf '  %-28s HTTP %-4s %8s B  🛑 200 SEM o conteudo (bloqueio disfarcado)\n' "$label" "${code:-?}" "${bytes:-0}"
  elif [ "$temConteudo" = "sim" ]; then
    printf '  %-28s HTTP %-4s %8s B  🛑 conteudo casa mas o CODIGO recusa\n' "$label" "${code:-?}" "${bytes:-0}"
  else
    printf '  %-28s HTTP %-4s %8s B  🛑 nao serve\n' "$label" "${code:-?}" "${bytes:-0}"
  fi
}

echo "=== 1. buscadores (HTML, sem JavaScript) ==="
probe "Google"            "https://www.google.com/search?q=cotacao+dolar&hl=pt-BR"   "<div|<h3"
probe "DuckDuckGo (html)" "https://html.duckduckgo.com/html/?q=cotacao+dolar"        "result__|result-link"
probe "DuckDuckGo (lite)" "https://lite.duckduckgo.com/lite/?q=cotacao+dolar"        "result-link|<a rel"
probe "Bing"              "https://www.bing.com/search?q=cotacao+dolar&setlang=pt-BR" "b_algo|<h2"
probe "Brave"             "https://search.brave.com/search?q=cotacao+dolar"          "snippet|result"
probe "Startpage"         "https://www.startpage.com/sp/search?query=cotacao+dolar"  "result|w-gl"
probe "Mojeek"            "https://www.mojeek.com/search?q=cotacao+dolar"            "results|<li"
probe "Wikipedia (API)"   "https://pt.wikipedia.org/w/api.php?action=opensearch&search=bitcoin&format=json" "bitcoin|Bitcoin"

echo
echo "=== 2. dado estruturado, sem autenticacao ==="
probe "AwesomeAPI USD-BRL"  "https://economia.awesomeapi.com.br/json/last/USD-BRL"                    '"USDBRL"'
probe "AwesomeAPI BTC-BRL"  "https://economia.awesomeapi.com.br/json/last/BTC-BRL"                    '"BTCBRL"'
probe "CoinGecko BTC"       "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd,brl" '"bitcoin"'
probe "Open-Meteo Rio"      "https://api.open-meteo.com/v1/forecast?latitude=-22.91&longitude=-43.17&current=temperature_2m&timezone=America/Sao_Paulo" '"temperature_2m"'
probe "wttr.in Rio (texto)" "https://wttr.in/Rio+de+Janeiro?format=j1"                                '"temp_C"'
probe "Banco Central PTAX"  "https://olinda.bcb.gov.br/olinda/servico/PTAX/versao/v1/odata/CotacaoDolarDia(dataCotacao=@dataCotacao)?@dataCotacao='08-29-2026'&\$format=json" '"cotacaoCompra"|"value"'

echo
echo "=== 3. esporte: quem joga no fim de semana ==="
probe "ESPN (API pública)"  "https://site.api.espn.com/apis/site/v2/sports/soccer/bra.1/scoreboard"   '"events"|"competitions"'
probe "Globo Esporte"       "https://ge.globo.com/futebol/"                                          "<article|feed-post"

echo
echo "=== 4. a data da propria maquina ==="
# "nesse domingo" so significa alguma coisa se o agente souber que dia e hoje --
# e o relogio do droplet e a unica fonte que nao depende de rede.
agent_ssh "LC_ALL=pt_BR.UTF-8 date '+%A, %d/%m/%Y %H:%M %Z' 2>/dev/null || date '+%A, %d/%m/%Y %H:%M %Z'" | sed 's/^/  /'
