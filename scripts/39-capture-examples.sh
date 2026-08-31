#!/bin/bash
# Captura a SAIDA REAL de cada comando que a documentacao promete.
#
# Existe porque exemplo em documentacao envelhece calado: a flag some, a
# mensagem muda, e o leitor descobre que o comando nao faz mais aquilo depois
# de rodar. Rodando as saidas de verdade, o que entra no README e o que a
# maquina responde hoje -- e este script vira a forma de conferir amanha.
#
# So comandos BARATOS: nenhum aqui chama o modelo. Os que chamam estao nas
# suites 30-32 e custam dinheiro por execucao.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

# Roda um comando na maquina e imprime com um cabecalho, para o recorte no
# README sair direto daqui sem edicao manual.
show() {
  local label="$1" command="$2"
  echo "=== $label"
  root_ssh "$command" 2>&1 | head -30
  echo
}

show "catalog list"       "sudo -u agentd agentd -catalog list"
show "vault-check"        "sudo -u agentd agentd -vault-check"
show "health"             "curl -sS http://127.0.0.1:8787/health"
show "agent-status"       "agent-status"
show "notify-drain"       "sudo -u agentd agentd -notify-drain"
show "runners cadastrados" "cat /workspace/agent/runners.json"
show "estado dos arquivos" "ls -l /workspace/agent/*.md /workspace/agent/*.log /workspace/agent/*.json 2>/dev/null"
show "progress.md (fim)"  "tail -5 /workspace/agent/progress.md"
show "activity.log (fim)" "tail -3 /workspace/agent/activity.log"

echo "erros: 0"
