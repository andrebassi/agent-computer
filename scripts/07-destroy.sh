#!/bin/bash
# Destrói o droplet. Exige confirmação digitada — é irreversível e leva junto
# tudo que estiver em /workspace e não tiver ido para um snapshot.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

id="$(droplet_id)"
[ -z "$id" ] && { echo "droplet nao existe — nada a destruir."; exit 0; }

echo "⚠️  vai DESTRUIR o droplet '$DROPLET_NAME' (id $id, IP $(droplet_ip))."
echo "    /workspace, perfil do navegador e sessões logadas se perdem."
echo "    Para guardar antes: scripts/04-snapshot.sh"
read -r -p "    digitar DESTROY para confirmar: " ok
[ "$ok" = "DESTROY" ] || { echo "cancelado"; exit 1; }

timeout 120s doctl compute droplet delete "$id" --force
echo "✅ destruido. Snapshots existentes NAO foram apagados (continuam cobrando US\$ 0,06/GB/mes):"
timeout 30s doctl compute snapshot list --resource droplet --format ID,Name,Size 2>/dev/null | grep -E "ID|$DROPLET_NAME" || true
