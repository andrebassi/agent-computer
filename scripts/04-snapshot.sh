#!/bin/bash
# Tira snapshot do droplet — é o que substitui o "Update/Reset" da doc do Grok,
# que o DigitalOcean tem nativo e o Fly não teria.
#
# O droplet é desligado antes: snapshot com a máquina ligada pode capturar o
# disco a meio de uma escrita, e o restore volta com o perfil do Chrome corrompido.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

id="$(droplet_id)"
[ -z "$id" ] && { echo "🛑 droplet nao existe"; exit 1; }

stamp="$(date +%Y%m%d-%H%M)"
snap="${DROPLET_NAME}-${stamp}"

echo "desligando droplet (snapshot a quente corrompe o perfil do navegador)"
timeout 180s doctl compute droplet-action power-off "$id" --wait

echo "criando snapshot '$snap' (leva alguns minutos)"
timeout 900s doctl compute droplet-action snapshot "$id" --snapshot-name "$snap" --wait

echo "religando droplet"
timeout 180s doctl compute droplet-action power-on "$id" --wait

echo "✅ snapshot '$snap' pronto"
timeout 30s doctl compute snapshot list --resource droplet --format Name,ID,Size,Created | grep -E "Name|$snap"
