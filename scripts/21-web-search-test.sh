#!/bin/bash
# Prova a habilidade de busca com as quatro perguntas que a motivaram.
#
# Sao quatro DE PROPOSITO, e nao uma: cada uma exercita um caminho diferente da
# habilidade, e uma so provaria um quarto dela.
#
#   dolar       -> atalho direto (Frankfurter), sem busca nenhuma
#   bitcoin     -> atalho direto (CoinGecko)
#   temperatura -> atalho direto (Open-Meteo), com coordenada
#   jogos       -> delegacao, porque nao ha atalho e o buscador bloqueia o IP
#
# A ultima e a unica que precisa do token de assinatura. As tres primeiras
# funcionam mesmo sem ele -- e e por isso que a habilidade poe atalho antes de
# busca: o caminho barato tambem e o que sobrevive a falta de credencial.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

xaiKey="$(timeout 25s pass show bassi/xai/apikey 2>/dev/null | head -1)"
[ -z "$xaiKey" ] && { echo "🛑 chave da xAI nao disponivel"; exit 1; }

echo "=== 0. instalando a habilidade no computador ==="
skillFile="$(dirname "$0")/../examples/skills/web-search.md"
agent_ssh "mkdir -p /workspace/agent/skills && cat > /workspace/agent/skills/web-search.md" < "$skillFile"
agent_ssh "wc -c /workspace/agent/skills/web-search.md" | sed 's/^/  /'

# A habilidade e referenciada com /<nome>, e o parser a anexa ao prompt.
run() {
  local label="$1" question="$2"
  echo
  echo "=== $label ==="
  agent_ssh "XAI_API_KEY='$xaiKey' /workspace/agentd -screen 2 -prompt \"/web-search $question\"" 2>&1 | tail -12
}

run "1. dolar (espera atalho: Frankfurter)"      "Qual a cotacao do dolar em reais agora? Diga o valor, a fonte e a data da cotacao."
run "2. bitcoin (espera atalho: CoinGecko)"      "Qual o preco do bitcoin agora, em dolar e em real? Diga a fonte."
run "3. temperatura (espera atalho: Open-Meteo)" "Qual a temperatura no Rio de Janeiro agora? Diga a fonte e a hora da medicao."
run "4. jogos (espera delegacao)"                "Quem joga futebol hoje no Brasileirao Serie A? Descubra primeiro que dia e hoje. Diga os jogos com horario e a fonte."
