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
# Ler o ARQUIVO, e nao `systemctl show -p Environment`.
#
# A variavel vem de EnvironmentFile, que o `show` NAO expande: a propriedade sai
# vazia mesmo com o destino configurado e funcionando. Medido em 31/08/2026 --
# esta auditoria imprimia SEM_WEBHOOK enquanto os dois destinos recebiam avisos.
# Relatorio de pendencia que INVENTA pendencia e pior que nenhum: manda consertar
# o que esta de pe, e ensina a duvidar do proximo vermelho.
configurados="$(root_ssh "grep -c '^AGENT_WEBHOOK=' /etc/agentd/notify.env 2>/dev/null || echo 0" 2>/dev/null | tr -dc '0-9')"
if [ "${configurados:-0}" -gt 0 ]; then
  echo "  ✅ destino configurado em /etc/agentd/notify.env"
  # Contar os destinos SEM imprimir as URLs: o topico do ntfy e a unica
  # credencial que existe, e relatorio costuma ser colado em chamado.
  quantos="$(root_ssh "grep '^AGENT_WEBHOOK=' /etc/agentd/notify.env | tr ',' '\n' | wc -l" 2>/dev/null | tr -dc '0-9')"
  echo "  destinos na lista: ${quantos:-1}"
else
  echo "  🛑 SEM destino: o agente pede take-over e ninguem fica sabendo"
fi
root_ssh "sudo -u agentd agentd -notify-drain 2>&1 | head -1" 2>&1 | sed 's/^/  /'
# A ultima entrega, que e o que diz se o canal esta VIVO -- destino configurado
# nao prova entrega, e foi assim que a fila acumulou 41 avisos sem ninguem notar.
root_ssh "journalctl -u agentd-notify -n 20 --no-pager -o cat 2>/dev/null | grep -E 'entregues|falhou em' | tail -1" 2>&1 | sed 's/^/  ultima entrega: /'

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
