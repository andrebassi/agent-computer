#!/bin/bash
# Restaura o droplet a partir de um snapshot — o "Recover/Reset" da doc.
# Uso: 05-restore.sh <snapshot-id>
#      05-restore.sh --latest
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

id="$(droplet_id)"
[ -z "$id" ] && { echo "🛑 droplet nao existe — use 01-create.sh"; exit 1; }

arg="${1:-}"
if [ -z "$arg" ]; then
  echo "uso: $0 <snapshot-id> | --latest"
  timeout 30s doctl compute snapshot list --resource droplet --format ID,Name,Size,Created \
    | grep -E "ID|${DROPLET_NAME}"
  exit 1
fi

if [ "$arg" = "--latest" ]; then
  # Ordena pela data no nome (AAAAMMDD-HHMM), que é monotônica por construção.
  snap_id="$(timeout 30s doctl compute snapshot list --resource droplet --format ID,Name --no-header \
    | grep "$DROPLET_NAME" | sort -k2 | tail -1 | awk '{print $1}')"
  [ -z "$snap_id" ] && { echo "🛑 nenhum snapshot de '$DROPLET_NAME'"; exit 1; }
else
  snap_id="$arg"
fi

echo "⚠️  restore SOBRESCREVE o disco atual do droplet $id com o snapshot $snap_id."
echo "    Tudo em /workspace gravado depois do snapshot se perde."
read -r -p "    digitar RESTORE para confirmar: " ok
[ "$ok" = "RESTORE" ] || { echo "cancelado"; exit 1; }

timeout 180s doctl compute droplet-action power-off "$id" --wait
timeout 900s doctl compute droplet-action restore "$id" --image-id "$snap_id" --wait
timeout 180s doctl compute droplet-action power-on "$id" --wait
echo "✅ restaurado. Conferir com 03-status.sh"
