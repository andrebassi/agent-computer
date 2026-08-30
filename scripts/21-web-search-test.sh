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
#
# # Por que este script foi reescrito em 30/08/2026
#
# A versao anterior NAO VERIFICAVA NADA. Disparava as quatro perguntas, imprimia
# "estado final: concluida" e saia com rc=0 sempre -- sem `fail`, sem contagem
# de erro, sem olhar a resposta. Passava com a habilidade desinstalada, com a
# resposta vazia e com a fonte errada.
#
# Pior: a instalacao da habilidade falhava com "Permission denied" (escrevia
# como `agent`, e o diretorio e do `agentd` por desenho) e o script seguia em
# frente. So funcionava porque a habilidade ja estava la de uma execucao antiga.
#
# Isto e a definicao de teste-teatro, e o comentario do `22-show-answers.sh` ja
# denunciava: "um teste que so olha o estado prova que a tarefa terminou, nunca
# que ela terminou CERTA".
#
# Agora ele confere as TRES coisas que importam, e cada uma pega um defeito que
# as outras deixam passar:
#
#   1. a tarefa concluiu               -> pega travamento e erro de execucao
#   2. a resposta tem numero           -> pega resposta evasiva ("nao consegui")
#   3. a FONTE esperada foi usada      -> pega o valor certo pelo caminho errado
#
# A terceira e a que justifica o teste existir. Um preco de bitcoin obtido por
# buscador em vez do atalho CoinGecko esta CERTO e nao se repete amanha -- o
# buscador bloqueia o IP de datacenter. Verificar so o numero aprovaria a rota
# que vai quebrar.
source "$(dirname "$0")/lib.sh"
source "$(dirname "$0")/suite-lock.sh"
suite_lock "$(basename "$0")"
set -uo pipefail
load_token

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

echo "=== 0. instalando a habilidade no computador ==="
skillFile="$(dirname "$0")/../examples/skills/web-search.md"

# Instalada como ROOT, com dono `agentd`. O diretorio e `agentd:agentd` 755 de
# proposito: habilidade e prompt que entra na conversa, entao deixar o usuario
# do modelo escrever ali seria deixa-lo reescrever as proprias instrucoes.
# A versao anterior usava `agent_ssh` e batia em "Permission denied" -- calada.
root_ssh "install -d -o agentd -g agentd -m 755 /workspace/agent/skills &&
  cat > /workspace/agent/skills/web-search.md &&
  chown agentd:agentd /workspace/agent/skills/web-search.md &&
  chmod 644 /workspace/agent/skills/web-search.md" < "$skillFile" \
  && ok "habilidade instalada como agentd" \
  || fail "nao consegui instalar a habilidade"

# Confere o TAMANHO contra o original: um `cat` truncado instala um arquivo que
# existe, tem dono certo e esta pela metade -- e a habilidade some do meio.
localBytes="$(wc -c < "$skillFile" | tr -d ' ')"
remoteBytes="$(agent_ssh "wc -c < /workspace/agent/skills/web-search.md" | tr -d ' \r')"
if [ "$localBytes" = "$remoteBytes" ]; then
  ok "tamanho confere: ${localBytes} bytes"
else
  fail "tamanho diverge: local ${localBytes}, remoto ${remoteBytes}"
fi

# Roda uma pergunta e VERIFICA a conversa gravada.
#
# `expectedSource` e um regex de alternativas porque a mesma fonte aparece com
# grafias diferentes no argumento da ferramenta e no texto da resposta
# (`api.frankfurter.app` na URL, "Frankfurter" na prosa).
run() {
  local label="$1" expectedSource="$2" question="$3"
  echo
  echo "=== $label ==="

  local output taskId
  output="$(agentd_run "-screen 2 -prompt \"/web-search $question\"" 2>&1)"

  # O id sai da saida do proprio comando; sem ele nao ha conversa para ler, e o
  # teste tem de reprovar em vez de conferir a conversa de OUTRA execucao --
  # que estaria la, gravada, e daria verde por engano.
  taskId="$(printf '%s' "$output" | sed -n 's/.*\(task-[0-9]\{6,\}\).*/\1/p' | head -1)"
  if [ -z "$taskId" ]; then
    fail "nao consegui extrair o id da tarefa"
    printf '%s\n' "$output" | tail -6 | sed 's/^/      /'
    return
  fi

  if printf '%s' "$output" | grep -q "concluída\|concluida"; then
    ok "tarefa $taskId concluiu"
  else
    fail "tarefa $taskId nao concluiu"
    printf '%s\n' "$output" | tail -6 | sed 's/^/      /'
    return
  fi

  # A resposta e as ferramentas vivem na conversa gravada, nao na saida.
  local report
  report="$(agent_ssh "python3 - /workspace/agent/conversations/${taskId}.json" <<'PYTHON'
import json, sys

with open(sys.argv[1]) as handle:
    conversation = json.load(handle)

toolText = []
answer = ""
for message in conversation.get("messages", []):
    for call in (message.get("ToolCalls") or []):
        toolText.append(call.get("Name", "") + " " + (call.get("Arguments") or ""))
    if message.get("Role") == "assistant" and (message.get("Content") or "").strip():
        answer = message["Content"].strip()

# Marcador de linha para o shell separar as tres informacoes sem ambiguidade:
# a resposta tem quebra de linha, entao ela vai por ultimo e inteira.
print("FERRAMENTAS:", " | ".join(toolText).replace("\n", " ")[:600])
print("RESPOSTA:", answer.replace("\n", " ")[:600])
PYTHON
)"

  local tools answer
  tools="$(printf '%s' "$report" | sed -n 's/^FERRAMENTAS: //p')"
  answer="$(printf '%s' "$report" | sed -n 's/^RESPOSTA: //p')"

  if [ -z "$answer" ]; then
    fail "a conversa nao tem resposta final"
    return
  fi

  # Numero na resposta: cotacao, preco e temperatura sao todos numericos, e
  # "nao consegui obter" e a forma que a falha assume quando a fonte cai.
  if printf '%s' "$answer" | grep -qE '[0-9]+[.,][0-9]+|[0-9]{3,}'; then
    ok "resposta tem valor numerico"
  else
    fail "resposta sem numero (evasiva?): ${answer:0:120}"
  fi

  # A FONTE, procurada nas ferramentas E na resposta: o atalho aparece na URL
  # da chamada, e a delegacao aparece so no texto.
  if printf '%s %s' "$tools" "$answer" | grep -qiE "$expectedSource"; then
    ok "usou a fonte esperada ($expectedSource)"
  else
    fail "fonte esperada ausente ($expectedSource)"
    echo "      ferramentas: ${tools:0:200}"
    echo "      resposta:    ${answer:0:200}"
  fi
}

run "1. dolar (espera atalho: Frankfurter)" \
    'frankfurter' \
    "Qual a cotacao do dolar em reais agora? Diga o valor, a fonte e a data da cotacao."

run "2. bitcoin (espera atalho: CoinGecko)" \
    'coingecko|coin_gecko' \
    "Qual o preco do bitcoin agora, em dolar e em real? Diga a fonte."

run "3. temperatura (espera atalho: Open-Meteo)" \
    'open-meteo|open_meteo|openmeteo' \
    "Qual a temperatura no Rio de Janeiro agora? Diga a fonte e a hora da medicao."

# A quarta nao tem atalho: espera-se que ela DELEGUE ao agente de codigo, ou que
# navegue. Aceita as duas rotas -- o que ela nao pode e responder de cabeca, e
# por isso a exigencia aqui e ter usado ALGUMA ferramenta de fora.
run "4. jogos (espera delegacao ou navegacao)" \
    'delegate|browser_|http_|search' \
    "Quem joga futebol hoje no Brasileirao Serie A? Descubra primeiro que dia e hoje. Diga os jogos com horario e a fonte."

echo
echo "erros: $errs"
exit $errs
