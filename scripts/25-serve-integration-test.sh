#!/bin/bash
# Testa a porta HTTP e a proatividade NA MAQUINA REAL.
#
# Existe porque duas garantias nao sao alcancaveis por teste em processo:
#
#   1. "o flock morre com o processo" e garantia do SISTEMA OPERACIONAL. Um
#      teste em processo nao a exercita, porque o descritor e dele proprio.
#      Provar a reconciliacao exige matar o processo de verdade.
#   2. "o aviso sobrevive a queda da sessao que iniciou a tarefa" exige que a
#      sessao caia de verdade.
#
# Nao aborta na primeira falha: soma os erros e devolve o total no rc, para uma
# execucao mostrar TODOS os problemas em vez de um por vez.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

ip="$(droplet_ip)"
[ -z "$ip" ] && { echo "🛑 droplet nao existe"; exit 1; }
echo "testando a porta HTTP em $ip"

# O token nunca chega por argumento: `ps` o exporia a qualquer processo da
# maquina, incluindo aos agentes que rodam nela.
apiCall() {
  local method="$1" path="$2" body="${3:-}"
  agent_ssh "timeout 15s curl -sS --max-time 10 -X $method \
    -H \"Authorization: Bearer \$(cat /workspace/agent/api-token)\" \
    -H 'Content-Type: application/json' \
    ${body:+-d '$body'} \
    -w '\nHTTP:%{http_code}' \
    http://127.0.0.1:8787$path"
}

echo
echo "=== 1. as unidades systemd existem e estao ativas ==="
for unit in agentd-api.service agentd-notify.timer; do
  state="$(agent_ssh "systemctl is-active $unit 2>/dev/null" | tr -d '\r')"
  [ "$state" = "active" ] && ok "$unit: $state" || fail "$unit: ${state:-sem resposta}"
done

echo
echo "=== 2. saude responde sem token ==="
health="$(agent_ssh "timeout 10s curl -sS --max-time 5 http://127.0.0.1:8787/health" 2>/dev/null)"
echo "$health" | grep -q '"status":"ok"' && ok "health: $health" || fail "health nao respondeu: ${health:-vazio}"

echo
echo "=== 3. a porta NAO escuta fora de loopback ==="
# A trava de seguranca mais importante: quem chega precisa ter passado pelo SSH.
listen="$(agent_ssh "ss -lnt 2>/dev/null | grep ':8787 '" | tr -d '\r')"
if echo "$listen" | grep -qE '127\.0\.0\.1:8787'; then
  ok "8787 so em loopback"
else
  fail "8787 EXPOSTA fora de loopback: ${listen:-nao esta ouvindo}"
fi

echo
echo "=== 4. sem token e 401 ==="
code="$(agent_ssh "timeout 10s curl -sS -o /dev/null -w '%{http_code}' --max-time 5 -X POST http://127.0.0.1:8787/tasks -d '{}'" | tr -d '\r')"
[ "$code" = "401" ] && ok "sem token: HTTP 401" || fail "sem token devia ser 401, veio $code"

echo
echo "=== 5. criar tarefa devolve 201 ==="
# A tarefa precisa DURAR: com `echo`, ela terminava antes do segundo POST e a
# tela ficava livre -- o teste do conflito recebia 201 e acusava o produto de um
# defeito que era do proprio teste (uma corrida entre as duas chamadas).
created="$(apiCall POST /tasks '{"prompt":"execute: sleep 45 && echo verificacao-ok","screen":2}')"
taskID="$(echo "$created" | grep -oE '"id":"[^"]+"' | head -1 | cut -d'"' -f4)"
if echo "$created" | grep -q "HTTP:201" && [ -n "$taskID" ]; then
  ok "tarefa criada: $taskID"
else
  fail "criacao falhou: $created"
fi

echo
echo "=== 6. segunda tarefa na MESMA tela devolve 409 com a dica ==="
conflict="$(apiCall POST /tasks '{"prompt":"outra","screen":2}')"
if echo "$conflict" | grep -q "HTTP:409" && echo "$conflict" | grep -q '"hint"'; then
  ok "409 com dica de como resolver"
else
  fail "esperava 409 com hint: $conflict"
fi

echo
echo "=== 7. consulta devolve estado e resposta ==="
if [ -n "${taskID:-}" ]; then
  # Espera a tarefa terminar; o teto evita pendurar se ela travar.
  for _ in $(seq 1 30); do
    state="$(apiCall GET "/tasks/$taskID" | grep -oE '"state":"[^"]+"' | cut -d'"' -f4)"
    case "$state" in done|failed|blocked) break ;; esac
    sleep 3
  done
  detail="$(apiCall GET "/tasks/$taskID")"
  echo "$detail" | grep -q "HTTP:200" && ok "consulta: estado=$state" || fail "consulta falhou: $detail"
  echo "$detail" | grep -q '"answer"' && ok "a resposta veio junto" || echo "  ⚠️  sem campo answer (estado $state)"
else
  fail "sem id de tarefa, pulando a consulta"
fi

echo
echo "=== 8. RECONCILIACAO: matar o processo e conferir que a tela volta ==="
# Este e o teste que so a maquina real permite.
recon="$(apiCall POST /tasks '{"prompt":"execute: sleep 300","screen":3}')"
reconID="$(echo "$recon" | grep -oE '"id":"[^"]+"' | head -1 | cut -d'"' -f4)"
if [ -z "$reconID" ]; then
  fail "nao consegui criar a tarefa da tela 3: $recon"
else
  sleep 3
  agent_ssh "sudo pkill -9 -f 'agentd -serve'" 2>/dev/null || true
  sleep 2
  # O disco precisa MANTER a tarefa como ativa: e o cadaver que a reconciliacao
  # vai encontrar.
  # Por ROOT, nao por `agent`: o diretorio de tarefas passou a ser do agentd, e
  # um `grep` sem permissao devolve vazio -- indistinguivel de "a tarefa sumiu".
  # Mesma licao do verificador do cofre: quem confere com o usuario RESTRITO nao
  # consegue separar ausencia de falta de acesso.
  # O JSON e gravado INDENTADO: `"State": "running"`, com espaco depois dos dois
  # pontos. Um padrao sem o espaco casa zero e devolve vazio -- que o teste lia
  # como "a tarefa sumiu do disco". Terceiro defeito seguido do verificador, e
  # nenhum do produto: quem verifica precisa ser verificado.
  onDisk="$(root_ssh "grep -oE '\"State\": *\"[^\"]*\"' /workspace/agent/tasks/$reconID.json 2>/dev/null" | cut -d'"' -f4)"
  case "$onDisk" in
    running|pending) ok "apos kill -9 o disco ainda diz '$onDisk' (o cadaver existe)" ;;
    *) fail "esperava running/pending no disco, veio '${onDisk:-nada}'" ;;
  esac

  agent_ssh "sudo systemctl start agentd-api && sleep 3" >/dev/null 2>&1
  after="$(apiCall GET "/tasks/$reconID" | grep -oE '"state":"[^"]+"' | cut -d'"' -f4)"
  [ "$after" = "failed" ] && ok "reconciliada no boot: $after" || fail "esperava failed apos reconciliar, veio '${after:-nada}'"

  again="$(apiCall POST /tasks '{"prompt":"execute: echo tela-liberada","screen":3}')"
  echo "$again" | grep -q "HTTP:201" && ok "a tela 3 voltou a aceitar tarefa" || fail "a tela nao foi liberada: $again"
fi

echo
echo "=== 9. PROATIVIDADE: o aviso sobrevive a queda da sessao ==="
# A fila e escrita pelo processo do servico, que nao depende da sessao SSH.
pending="$(agent_ssh "/usr/local/bin/agentd -notify-drain -state /workspace/agent 2>&1" | tr -d '\r')"
if echo "$pending" | grep -qE "aviso\(s\) pendente|nenhum aviso"; then
  ok "drenador responde: $(echo "$pending" | head -1)"
else
  fail "drenador nao respondeu como esperado: $pending"
fi
# Uma tarefa que falhou ja deve ter enfileirado um aviso.
if agent_ssh "test -s /workspace/agent/events/events.jsonl" 2>/dev/null; then
  count="$(agent_ssh "wc -l < /workspace/agent/events/events.jsonl" | tr -d ' \r')"
  ok "a fila tem $count aviso(s) gravado(s) pelo servico"
else
  echo "  ⚠️  fila vazia (nenhuma tarefa bloqueou ou falhou ainda)"
fi

echo
echo "=== 10. o timer de avisos esta armado ==="
timer="$(agent_ssh "systemctl list-timers agentd-notify.timer --no-pager 2>/dev/null | grep agentd-notify" | tr -d '\r')"
[ -n "$timer" ] && ok "timer armado" || fail "timer nao aparece na lista"

echo
echo "erros: $errs"
exit $errs
