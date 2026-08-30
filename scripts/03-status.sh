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
timeout 30s doctl compute snapshot list --resource droplet --format Name,ID,Size,Created 2>/dev/null \
  | grep -E "Name|${DROPLET_NAME}" || echo "  nenhum snapshot"
