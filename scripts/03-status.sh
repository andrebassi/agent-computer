#!/bin/bash
# Estado do droplet (fora) e dos serviços da tela (dentro).
source "$(dirname "$0")/lib.sh"
set +e
load_token

echo "=== droplet ==="
timeout 30s doctl compute droplet list --format Name,ID,PublicIPv4,Region,Memory,Status,Tags 2>/dev/null \
  | awk -v n="$DROPLET_NAME" 'NR==1 || $1==n'

ip="$(droplet_ip)"
[ -z "$ip" ] && { echo; echo "droplet nao existe."; exit 0; }

echo
echo "=== dentro da maquina ==="
agent_ssh 'agent-status' 2>&1 | sed 's/^/  /'

echo
echo "=== snapshots ==="
# A coluna se chama CreatedAt, nao Created. Com o nome errado o doctl aborta, e
# o 2>/dev/null que havia aqui transformava o erro em "nenhum snapshot" -- o
# falso negativo que a rule 13 chama de pior que o falso positivo, porque manda
# recriar o que ja existe.
timeout 30s doctl compute snapshot list --resource droplet --format Name,ID,Size,CreatedAt \
  | grep -E "Name|${DROPLET_NAME}" || echo "  nenhum snapshot de droplet"
timeout 30s doctl compute snapshot list --resource volume --format Name,ID,Size,CreatedAt \
  | grep -E "Name|${VOLUME_NAME}" || echo "  nenhum snapshot de volume"
