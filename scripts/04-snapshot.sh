#!/bin/bash
# Tira snapshot do droplet — é o que substitui o "Update/Reset" da doc do Grok,
# que o DigitalOcean tem nativo e o Fly não teria.
#
# O droplet é desligado antes: snapshot com a máquina ligada pode capturar o
# disco a meio de uma escrita, e o restore volta com o perfil do Chrome corrompido.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

# Snapshot do VOLUME, nao do droplet: depois que /workspace passou a morar num
# volume separado, o disco do droplet virou descartavel por construcao -- nada
# durave mora la. Snapshot de volume tambem e mais barato e muito mais rapido.
vol_id="$(timeout 30s doctl compute volume list --format ID,Name --no-header \
  | awk -v n="$VOLUME_NAME" '$2==n{print $1}')"
[ -z "$vol_id" ] && { echo "volume '$VOLUME_NAME' nao existe"; exit 1; }

stamp="$(date +%Y%m%d-%H%M)"
snap="${VOLUME_NAME}-${stamp}"

# sync antes: o snapshot le o volume como esta no disco, e escrita ainda em
# cache de pagina nao entra nele.
id="$(droplet_id)"
if [ -n "$id" ]; then
  echo "descarregando cache de escrita no volume"
  agent_ssh 'sync; sudo sync' 2>/dev/null || true
fi

echo "criando snapshot '$snap'"
timeout 900s doctl compute volume snapshot "$vol_id" --snapshot-name "$snap" --format ID,Name,Size

echo "OK. Snapshots do volume:"
timeout 30s doctl compute snapshot list --resource volume --format Name,ID,Size,Created | grep -E "Name|$VOLUME_NAME"
