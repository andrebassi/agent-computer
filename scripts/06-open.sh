#!/bin/bash
# Abre a tela do agente no navegador local, por túnel SSH.
#
# Nada da tela é publicado na internet: o noVNC escuta em 127.0.0.1 dentro do
# droplet, e este túnel é o único caminho. Quem não tem a chave SSH não chega.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

ip="$(droplet_ip)"
[ -z "$ip" ] && { echo "🛑 droplet nao existe — rodar 01-create.sh"; exit 1; }

# Derruba túnel anterior para a mesma porta, senão o ssh falha com "Address already in use"
# e o usuário fica olhando uma tela velha achando que é a nova.
existing="$(lsof -ti tcp:${LOCAL_VNC_PORT} 2>/dev/null || true)"
if [ -n "$existing" ]; then
  echo "derrubando tunel anterior na porta ${LOCAL_VNC_PORT} (pid $existing)"
  kill $existing 2>/dev/null || true
  sleep 1
fi

echo "abrindo tunel para $ip"
ssh -i "$SSH_KEY_FILE" \
    -o StrictHostKeyChecking=accept-new \
    -o ExitOnForwardFailure=yes \
    -o ServerAliveInterval=30 \
    -N -f \
    -L "${LOCAL_VNC_PORT}:127.0.0.1:6081" \
    -L "${LOCAL_CDP_PORT}:127.0.0.1:9221" \
    "agent@${ip}"

sleep 2
url="http://127.0.0.1:${LOCAL_VNC_PORT}/vnc.html?autoconnect=true&resize=scale"
echo "✅ tunel no ar"
echo "   tela : $url"
echo "   CDP  : http://127.0.0.1:${LOCAL_CDP_PORT}/json/version"
echo "   fechar: kill \$(lsof -ti tcp:${LOCAL_VNC_PORT})"
open "$url" 2>/dev/null || echo "   (abrir manualmente: $url)"
