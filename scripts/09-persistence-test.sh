#!/bin/bash
# Prova a promessa central do modelo: a sessao do navegador sobrevive ao
# desligamento da maquina. Reinicia o droplet e confere que (a) os servicos
# sobem sozinhos e (b) o historico gravado antes continua la depois.
#
# Sem este teste, "perfil persistente" e so uma linha de configuracao — a
# unica prova e desligar de verdade e olhar o que voltou.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

ip="$(droplet_ip)"
[ -z "$ip" ] && { echo "droplet nao existe"; exit 1; }
errs=0

echo "=== ANTES: historico do Chrome ==="
# O arquivo fica travado com o Chrome no ar; copiar antes de consultar.
antes="$(agent_ssh 'cp /workspace/.browser/Default/History /tmp/h.db 2>/dev/null && \
  sqlite3 /tmp/h.db "select count(*) from urls" 2>/dev/null || echo "0"')"
urls_antes="$(agent_ssh 'sqlite3 /tmp/h.db "select url from urls order by id desc limit 3" 2>/dev/null')"
echo "  entradas: $antes"
echo "$urls_antes" | sed 's/^/    /'
tam_antes="$(agent_ssh 'du -sb /workspace/.browser 2>/dev/null | cut -f1')"
echo "  perfil: $tam_antes bytes"

echo
echo "=== reiniciando o droplet ==="
agent_ssh 'sudo systemctl reboot' 2>/dev/null || true
sleep 20

echo "esperando voltar"
for i in $(seq 1 30); do
  if agent_ssh 'systemctl is-system-running 2>/dev/null | grep -qE "running|degraded"' 2>/dev/null; then
    echo "  voltou ($(date +%H:%M:%S))"
    break
  fi
  echo "  ... ainda offline ($(date +%H:%M:%S))"
  sleep 10
done

echo
echo "=== DEPOIS: servicos subiram sozinhos? ==="
for unit in xvfb openbox x11vnc novnc chrome; do
  st="$(agent_ssh "systemctl is-active ${unit}.service 2>/dev/null")"
  if [ "$st" = "active" ]; then echo "  OK   $unit"; else echo "  FALHA $unit: ${st:-sem resposta}"; errs=$((errs+1)); fi
done

echo
echo "=== DEPOIS: historico sobreviveu? ==="
depois="$(agent_ssh 'cp /workspace/.browser/Default/History /tmp/h2.db 2>/dev/null && \
  sqlite3 /tmp/h2.db "select count(*) from urls" 2>/dev/null || echo "0"')"
urls_depois="$(agent_ssh 'sqlite3 /tmp/h2.db "select url from urls order by id desc limit 3" 2>/dev/null')"
echo "  entradas: $depois (antes: $antes)"
echo "$urls_depois" | sed 's/^/    /'
if [ "${depois:-0}" -ge "${antes:-0}" ] && [ "${depois:-0}" -gt 0 ] 2>/dev/null; then
  echo "  OK   historico preservado"
else
  echo "  FALHA historico perdido no reboot"; errs=$((errs+1))
fi

tam_depois="$(agent_ssh 'du -sb /workspace/.browser 2>/dev/null | cut -f1')"
echo "  perfil: $tam_depois bytes (antes: $tam_antes)"

echo
echo "erros: $errs"
exit $errs
