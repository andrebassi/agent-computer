#!/bin/bash
# Sonda, EM PARALELO, as fontes que faltaram: cambio USD-BRL e jogos.
#
# Paralelo, e nao em serie, porque a sonda anterior levou ~4 min para 16 fontes
# em fila -- e a proxima pessoa que quiser acrescentar uma fonte nao vai rodar
# uma sonda que demora isso. Aqui cada fonte tem 8 s e todas correm juntas.
#
# 8 s tambem e o prazo que a habilidade vai usar: fonte lenta e fonte
# descartada. Sondar com 20 s e prometer 8 s mediria a coisa errada.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

echo "sondando cambio e jogos, em paralelo, com 8s por fonte"
echo

# O laco inteiro vai num unico SSH: abrir uma conexao por fonte gastaria mais
# tempo em handshake do que na propria consulta.
agent_ssh 'bash -s' <<'REMOTO'
set -u
UA="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

probe() {
  local label="$1" url="$2" proof="$3"
  local body code
  body="$(curl -sS -L --max-time 8 -A "$UA" -w '\nHTTPCODE:%{http_code}' "$url" 2>&1)"
  code="$(printf '%s' "$body" | grep -o 'HTTPCODE:[0-9]*' | cut -d: -f2)"
  local hit="nao"
  printf '%s' "$body" | grep -qiE "$proof" && hit="sim"
  if [ "$code" = "200" ] && [ "$hit" = "sim" ]; then
    printf '  %-26s HTTP %-4s ✅ serve  | %s\n' "$label" "$code" "$(printf '%s' "$body" | head -c 150 | tr -d '\n')"
  else
    printf '  %-26s HTTP %-4s 🛑\n' "$label" "${code:-timeout}"
  fi
}

echo "=== cambio USD-BRL, sem autenticacao ==="
probe "Frankfurter"      "https://api.frankfurter.app/latest?from=USD&to=BRL"                 '"BRL"' &
probe "open.er-api"      "https://open.er-api.com/v6/latest/USD"                              '"BRL"' &
probe "exchangerate.host" "https://api.exchangerate.host/latest?base=USD&symbols=BRL"         '"BRL"|"rates"' &
probe "CoinGecko USD-BRL" "https://api.coingecko.com/api/v3/simple/price?ids=tether&vs_currencies=brl" '"tether"' &
probe "BCB PTAX (hoje)"  "https://olinda.bcb.gov.br/olinda/servico/PTAX/versao/v1/odata/CotacaoDolarPeriodo(dataInicial=@dataInicial,dataFinalCotacao=@dataFinalCotacao)?@dataInicial='08-25-2026'&@dataFinalCotacao='08-29-2026'&\$top=1&\$format=json&\$orderby=dataHoraCotacao%20desc" '"cotacaoCompra"' &
wait

echo
echo "=== jogos de futebol ==="
probe "API-Futebol (livre)" "https://api.futebol.com.br/v1/campeonatos"                        '"campeonato"|"nome"' &
probe "TheSportsDB"         "https://www.thesportsdb.com/api/v1/json/3/eventsday.php?d=2026-08-30&s=Soccer" '"events"' &
probe "OpenLigaDB"          "https://api.openligadb.de/getmatchdata/bl1"                        '"matchID"|"MatchID"' &
probe "Bing (jogos hoje)"   "https://www.bing.com/search?q=jogos+de+hoje+brasileirao&setlang=pt-BR" 'b_algo' &
wait

echo
echo "=== extracao: o HTML do Bing vira texto util? ==="
# 124 KB de HTML nao servem ao modelo. O que interessa e se da para reduzir a
# algumas linhas de titulo e trecho sem precisar de biblioteca.
curl -sS -L --max-time 8 -A "$UA" "https://www.bing.com/search?q=cotacao+do+dolar+hoje&setlang=pt-BR" 2>/dev/null \
  | sed -e 's/<[^>]*>/ /g' -e 's/&nbsp;/ /g' -e 's/&amp;/\&/g' -e 's/&#160;/ /g' \
  | tr -s ' ' | grep -oE '[^ ].{0,160}' | grep -iE "d[oó]lar|R\\$|cota" | head -5 | sed 's/^/  /'
REMOTO
