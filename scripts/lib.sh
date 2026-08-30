#!/bin/bash
# Constantes e utilidades compartilhadas pelos scripts do agent computer.
#
# ATENÇÃO: este arquivo é SOURCEADO. Não usar `set -e` aqui — o flag vaza para
# o shell que chamou e mata o script na primeira saída não-zero legítima
# (um `grep` que não acha nada, por exemplo), sem imprimir erro nenhum.

# Identidade do droplet no DigitalOcean.
DROPLET_NAME="${DROPLET_NAME:-agent-computer}"
DROPLET_REGION="${DROPLET_REGION:-nyc3}"        # menor latência daqui entre as regiões DO
DROPLET_SIZE="${DROPLET_SIZE:-s-2vcpu-4gb}"     # US$ 24,00/mês — 2 GB faz o Chrome estourar
DROPLET_IMAGE="${DROPLET_IMAGE:-ubuntu-24-04-x64}"
SSH_KEY_ID="${SSH_KEY_ID:-55207659}"            # andrebassi-bb, única chave da conta
SSH_KEY_FILE="${SSH_KEY_FILE:-$HOME/.ssh/andrebassi-bb}"

# Estado duravel: volume de bloco separado do disco do droplet. E o que permite
# o "Update" da doc -- trocar a imagem do computador sem perder o trabalho.
# US$ 0,10/GB/mes.
VOLUME_NAME="${VOLUME_NAME:-agent-computer-workspace}"
VOLUME_SIZE_GB="${VOLUME_SIZE_GB:-20}"

# Portas do túnel local. A tela nunca é publicada — só chega por SSH.
LOCAL_VNC_PORT="${LOCAL_VNC_PORT:-6081}"   # 6080 + numero da tela
LOCAL_CDP_PORT="${LOCAL_CDP_PORT:-9221}"   # 9220 + numero da tela

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="${PROJECT_DIR}/logs"
mkdir -p "$LOG_DIR"

# Carrega o token do pass e o exporta. Nunca imprime o valor.
load_token() {
  if [ -n "${DIGITALOCEAN_ACCESS_TOKEN:-}" ]; then return 0; fi
  DIGITALOCEAN_ACCESS_TOKEN="$(timeout 25s pass show bassi/digitalocean/api-token 2>/dev/null | head -1)"
  export DIGITALOCEAN_ACCESS_TOKEN
  if [ -z "$DIGITALOCEAN_ACCESS_TOKEN" ]; then
    echo "🛑 token DigitalOcean vazio — o gpg-agent provavelmente está travado." >&2
    echo "   Teste real (listar chave NÃO prova nada):" >&2
    echo "   echo x | timeout 8s gpg --batch --sign -o /dev/null -" >&2
    return 1
  fi
}

# Devolve o IP público do droplet, ou vazio se ele não existir.
droplet_ip() {
  timeout 30s doctl compute droplet list --format Name,PublicIPv4 --no-header 2>/dev/null \
    | awk -v n="$DROPLET_NAME" '$1==n {print $2}' | head -1
}

# Devolve o ID do droplet, ou vazio.
droplet_id() {
  timeout 30s doctl compute droplet list --format Name,ID --no-header 2>/dev/null \
    | awk -v n="$DROPLET_NAME" '$1==n {print $2}' | head -1
}

# Atalho de SSH com as opções que evitam travar em prompt interativo.
agent_ssh() {
  local ip; ip="$(droplet_ip)"
  [ -z "$ip" ] && { echo "🛑 droplet '$DROPLET_NAME' não existe" >&2; return 1; }
  ssh -i "$SSH_KEY_FILE" \
      -o StrictHostKeyChecking=accept-new \
      -o UserKnownHostsFile="$HOME/.ssh/known_hosts" \
      -o ConnectTimeout=10 \
      "agent@${ip}" "$@"
}
