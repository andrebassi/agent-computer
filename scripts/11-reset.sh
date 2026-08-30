#!/bin/bash
# RESET (verbo da doc): volta ao snapshot duravel mais recente e DESCARTA o
# trabalho recente.
#
# Diferenca em relacao ao update: o update preserva /workspace como esta agora;
# o reset devolve /workspace ao que era no snapshot. E a operacao destrutiva
# dos tres, e a doc e explicita quanto a isso.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

vol_id="$(timeout 30s doctl compute volume list --format ID,Name --no-header \
  | awk '$2=="agent-computer-workspace"{print $1}')"
[ -z "$vol_id" ] && { echo "volume nao existe"; exit 1; }

snaps="$(timeout 30s doctl compute snapshot list --resource volume --format ID,Name,Created --no-header 2>/dev/null | grep agent-computer || true)"
if [ -z "$snaps" ]; then
  echo "🛑 nenhum snapshot do volume. Criar um antes: scripts/04-snapshot.sh"
  exit 1
fi
echo "snapshots disponiveis:"
echo "$snaps" | sed 's/^/  /'
echo
snap_id="${1:-$(echo "$snaps" | sort -k2 | tail -1 | awk '{print $1}')}"
echo "vai restaurar: $snap_id"
echo "⚠️  TUDO gravado em /workspace depois desse snapshot SE PERDE."
read -r -p "   digitar RESET para confirmar: " ok
[ "$ok" = "RESET" ] || { echo "cancelado"; exit 1; }

id="$(droplet_id)"
if [ -n "$id" ]; then
  agent_ssh 'sudo systemctl stop "chrome@*" "novnc@*" "x11vnc@*" "openbox@*" "xvfb@*" 2>/dev/null; sync' || true
  timeout 180s doctl compute volume-action detach "$vol_id" "$id" --wait
fi

# O DigitalOcean nao restaura um snapshot POR CIMA de um volume existente: o
# caminho e criar um volume novo a partir do snapshot e trocar o nome. Por isso
# o volume antigo so e apagado depois que o novo existe.
echo "criando volume a partir do snapshot"
novo="$(timeout 180s doctl compute volume create agent-computer-workspace-restored \
  --region "$DROPLET_REGION" --snapshot "$snap_id" --format ID --no-header)"
echo "  volume novo: $novo"

echo "removendo volume antigo e renomeando"
timeout 120s doctl compute volume delete "$vol_id" --force
# doctl nao tem rename de volume; a API tem.
curl -sS -X PUT "https://api.digitalocean.com/v2/volumes/${novo}" \
  -H "Authorization: Bearer ${DIGITALOCEAN_ACCESS_TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"name":"agent-computer-workspace"}' >/dev/null

if [ -n "$id" ]; then
  timeout 180s doctl compute volume-action attach "$novo" "$id" --wait
  agent_ssh 'sudo mount -a && sudo systemctl start xvfb@1 openbox@1 x11vnc@1 novnc@1 chrome@1'
fi
echo "✅ reset concluido"
