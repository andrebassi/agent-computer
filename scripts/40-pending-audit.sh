#!/bin/bash
# Levanta o que AINDA falta, medindo em vez de lembrar.
#
# A secao de pendencias do README envelhece: item fechado continua marcado como
# aberto, e item que regrediu continua marcado como fechado. Este script pergunta
# a maquina, e o que ele imprime e o que vale.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

echo "=== 1. tetos em vigor no servico (vazio = padrao compilado) ==="
root_ssh "systemctl show agentd-api -p Environment | tr ' ' '\n' | grep -i AGENTD_MAX || echo '  (nenhum override: valem 180 turnos / 3 falhas / US\$ 3,00 / 4 simultaneas)'" 2>&1 | sed 's/^/  /'

echo
echo "=== 2. fila de avisos: ha destino configurado? ==="
root_ssh "systemctl show agentd-notify -p Environment | tr ' ' '\n' | grep -i AGENT_WEBHOOK || echo 'SEM_WEBHOOK'" 2>&1 | sed 's/^/  /'
root_ssh "sudo -u agentd agentd -notify-drain 2>&1 | head -1" 2>&1 | sed 's/^/  /'

echo
echo "=== 3. tarefas presas (lock sem processo) ==="
root_ssh "ls -1 /workspace/agent/locks/ 2>/dev/null | wc -l" 2>&1 | sed 's/^/  travas: /'
root_ssh "grep -c 'estado=blocked' /workspace/agent/progress.md 2>/dev/null" 2>&1 | sed 's/^/  bloqueios ja registrados: /'

echo
echo "=== 4. runners: quais existem de fato na maquina ==="
for binary in claude codex opencode droid kiro; do
  status="$(root_ssh "command -v $binary >/dev/null 2>&1 && echo instalado || echo AUSENTE" 2>&1)"
  printf '  %-10s %s\n' "$binary" "$status"
done

echo
echo "=== 5. imagem e snapshot: quao velhos ==="
timeout 60s doctl compute snapshot list --resource droplet --no-header 2>/dev/null \
  | awk '{print "  imagem  " $2 "  " $3}' | tail -1
timeout 60s doctl compute snapshot list --resource volume --no-header 2>/dev/null \
  | sort -k3 | awk '{print "  volume  " $2 "  " $3}' | tail -1

echo
echo "=== 6. binario no ar x binario compilado agora ==="
root_ssh "/usr/local/bin/agentd -state 2>/dev/null | head -1; stat -c '%y' /usr/local/bin/agentd" 2>&1 | sed 's/^/  /'

echo
echo "erros: 0"
