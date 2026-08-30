#!/bin/bash
# Cria o droplet do agent computer.
#
# POR QUE PELA API E NAO POR `doctl compute droplet create --user-data-file`:
# o doctl corrompe o user-data por dupla codificacao UTF-8. Medido em 29/08/2026
# nas versoes 1.145.0 E 1.167.0: um "acessivel" com acento sai do disco como
# C3 AD e chega no droplet como C3 83 C2 AD. O byte C2 80 que o em-dash
# duplo-codificado gera e um caractere de controle C1, que o cloud-init recusa —
# e a recusa e SILENCIOSA: ele reporta "status: done", nao instala nada, e so
# quem for ler os recoverable_errors (que saem no stderr) descobre.
# JSON e UTF-8 por especificacao, entao o jq monta o corpo com a codificacao
# preservada e o problema desaparece na origem.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

if [ -n "$(droplet_id)" ]; then
  echo "ℹ️  droplet '$DROPLET_NAME' ja existe (IP $(droplet_ip)) — nada a fazer."
  exit 0
fi

# Dois caminhos, e o gate abaixo vale para os dois.
#
# O do NixOS é GERADO na hora a partir de `nixos/`, e não versionado: manter uma
# cópia do módulo dentro de um YAML seria manter duas verdades, e a que fica para
# trás sobe sem erro nenhum — as duas são YAML válido.
case "$AGENT_OS" in
  ubuntu)
    USER_DATA="${PROJECT_DIR}/cloud-init/user-data.yaml"
    ;;
  nixos)
    echo "montando o cloud-init do NixOS a partir de nixos/"
    USER_DATA="$(mktemp "${TMPDIR:-/tmp}/agent-computer-nixos.XXXXXX.yaml")"
    trap 'rm -f "$USER_DATA"' EXIT
    "${PROJECT_DIR}/scripts/29-render-nixos-userdata.sh" > "$USER_DATA"
    ;;
esac
echo "sistema: $AGENT_OS"

# Gate: YAML invalido so apareceria depois de o droplet existir e cobrar.
echo "validando cloud-init antes de enviar"
python3 - "$USER_DATA" <<'PYCHECK'
import sys, yaml, pathlib
raw = pathlib.Path(sys.argv[1]).read_bytes()
try:
    text = raw.decode("utf-8")
except UnicodeDecodeError as e:
    sys.exit(f"  UTF-8 invalido: {e}")
# O DigitalOcean corrompe user_data com QUALQUER byte nao-ASCII (dupla
# codificacao UTF-8 no caminho API -> ConfigDrive), e o cloud-init entao
# recusa o arquivo inteiro em silencio. Reproduzido em doctl 1.145.0, 1.167.0
# e na API REST direta. Por isso o gate e ASCII estrito, nao apenas C1.
nao_ascii = [(i, repr(c), hex(ord(c))) for i, c in enumerate(text) if ord(c) > 127]
if nao_ascii:
    i, ch, code = nao_ascii[0]
    sys.exit(f"  byte nao-ASCII na posicao {i}: {ch} ({code}) — "
             f"o DigitalOcean vai corromper e o cloud-init vai recusar calado. "
             f"Total: {len(nao_ascii)}. Ver o aviso no topo do user-data.yaml.")
d = yaml.safe_load(text)
print(f"  OK: {len(d.get('packages',[]))} pacotes, "
      f"{len(d.get('write_files',[]))} arquivos, {len(d.get('runcmd',[]))} comandos")
PYCHECK

echo "criando '$DROPLET_NAME': $DROPLET_SIZE em $DROPLET_REGION ($DROPLET_IMAGE)"

# O volume duravel e anexado JA NA CRIACAO: se fosse anexado depois, o
# cloud-init rodaria antes de o device existir e criaria /workspace no disco
# do droplet -- tudo funcionaria, e o estado sumiria no primeiro update.
vol_id="$(timeout 30s doctl compute volume list --format ID,Name --no-header 2>/dev/null \
  | awk -v n="$VOLUME_NAME" '$2==n{print $1}')"
if [ -z "$vol_id" ]; then
  echo "  volume duravel '$VOLUME_NAME' nao existe; criando"
  vol_id="$(timeout 120s doctl compute volume create "$VOLUME_NAME" \
    --region "$DROPLET_REGION" --size "${VOLUME_SIZE_GB}GiB" --fs-type ext4 \
    --format ID --no-header)"
fi
echo "  volume duravel: $vol_id"

body="$(jq -n \
  --arg name "$DROPLET_NAME" \
  --arg region "$DROPLET_REGION" \
  --arg size "$DROPLET_SIZE" \
  --arg image "$DROPLET_IMAGE" \
  --argjson key "$SSH_KEY_ID" \
  --arg vol "$vol_id" \
  --rawfile ud "$USER_DATA" \
  '{name:$name, region:$region, size:$size, image:$image,
    ssh_keys:[$key], user_data:$ud, monitoring:true,
    volumes:[$vol],
    tags:["agent-computer","lab"]}')"

resp="$(curl -sS -X POST "https://api.digitalocean.com/v2/droplets" \
  -H "Authorization: Bearer ${DIGITALOCEAN_ACCESS_TOKEN}" \
  -H "Content-Type: application/json; charset=utf-8" \
  --data-binary "$body")"

id="$(echo "$resp" | jq -r '.droplet.id // empty')"
if [ -z "$id" ]; then
  echo "🛑 API recusou a criacao:"
  echo "$resp" | jq . 2>/dev/null || echo "$resp"
  exit 1
fi
echo "  droplet id $id criado, esperando ficar ativo"

# Esperar status active — a API devolve "new" na criacao.
for _ in $(seq 1 40); do
  st="$(curl -sS -H "Authorization: Bearer ${DIGITALOCEAN_ACCESS_TOKEN}" \
        "https://api.digitalocean.com/v2/droplets/${id}" | jq -r '.droplet.status')"
  [ "$st" = "active" ] && break
  sleep 5
done

timeout 30s doctl compute droplet get "$id" --format ID,Name,PublicIPv4,Region,Memory,Status

echo
echo "✅ droplet criado. O cloud-init ainda esta instalando — rodar 02-wait-ready.sh."
