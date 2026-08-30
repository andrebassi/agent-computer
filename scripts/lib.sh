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

# Qual sistema o droplet recebe. São DOIS caminhos, e os dois valem.
#
#   ubuntu  cloud-init imperativo, 658 linhas — o padrão, e é o verificado
#   nixos   configuração declarativa em nixos/host.nix, instalada sobre o Ubuntu
#
# O padrão continua sendo `ubuntu` de propósito: ele é o caminho que já passou
# pelas três suítes, e mantê-lo intacto é o que torna o NixOS uma escolha em vez
# de uma aposta. Se o NixOS não subir, voltar custa uma variável:
#
#   task destroy && AGENT_OS=ubuntu task up
#
# ⚠️ A imagem do droplet continua sendo Ubuntu mesmo com `AGENT_OS=nixos`, e
# isso é deliberado: **não existe imagem NixOS oficial no DigitalOcean**. A
# máquina nasce Ubuntu e um instalador (`nixos-infect`) põe NixOS por cima, no
# lugar, ainda no primeiro boot — instala o Nix, constrói o sistema a partir de
# `nixos/host.nix`, reescreve o boot e reinicia nele.
#
# Não trocar DROPLET_IMAGE achando que ajuda: não há para o quê trocar.
AGENT_OS="${AGENT_OS:-ubuntu}"
case "$AGENT_OS" in
  ubuntu|nixos) ;;
  *) echo "🛑 AGENT_OS inválido: '$AGENT_OS' (use 'ubuntu' ou 'nixos')" >&2; return 1 2>/dev/null || exit 1 ;;
esac

# agent_os descobre qual sistema a máquina NO AR está rodando.
#
# Não confunde com $AGENT_OS: aquele é o que se quer criar, este é o que existe.
# Os dois divergem o tempo todo — ao trocar de caminho, ao rodar um teste contra
# uma máquina criada ontem. Verificação que lê a variável em vez de perguntar à
# máquina reporta o firewall errado e acusa defeito onde não há.
agent_os() {
  local osID
  osID="$(agent_ssh '. /etc/os-release 2>/dev/null && echo "$ID"' 2>/dev/null | tr -d '\r')"
  case "$osID" in
    nixos)  echo "nixos" ;;
    ubuntu) echo "ubuntu" ;;
    *)      echo "desconhecido" ;;
  esac
}

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

# Nome do computador na malha Tailscale, quando ele estiver nela.
TAILSCALE_HOSTNAME="${TAILSCALE_HOSTNAME:-agent-computer}"

# Devolve o melhor endereço para alcançar o computador.
#
# Prefere a malha ao IP público por um motivo prático, não estético: o IP
# público MUDA a cada rebuild do droplet, e o endereço da malha não. Cinco
# reconstruções num dia produziram cinco IPs diferentes, e cada uma invalidou
# comando anotado, entrada de known_hosts e túnel aberto.
#
# A escolha é silenciosa e com reserva: se a malha não estiver no ar, ou o nó
# estiver offline nela, cai para o IP público sem reclamar. Uma malha caída não
# pode impedir o acesso a uma máquina que está de pé.
agent_host() {
  # `tailscale status` sai com código diferente de zero quando o serviço está
  # parado, e é assim que se distingue "malha caída" de "nó offline".
  if command -v tailscale >/dev/null 2>&1; then
    local meshAddress
    meshAddress="$(timeout 8s tailscale status --json 2>/dev/null | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
except Exception:
    raise SystemExit
if data.get('BackendState') != 'Running':
    raise SystemExit
for peer in (data.get('Peer') or {}).values():
    nome = (peer.get('HostName') or '').split('.')[0]
    if nome == '$TAILSCALE_HOSTNAME' and peer.get('Online'):
        ips = peer.get('TailscaleIPs') or []
        if ips:
            print(ips[0])
        break
" 2>/dev/null)"
    if [ -n "$meshAddress" ]; then
      echo "$meshAddress"
      return 0
    fi
  fi
  droplet_ip
}

# Diz por qual caminho o acesso está indo, para o diagnóstico não virar
# adivinhação quando algo falhar.
agent_route() {
  local host; host="$(agent_host)"
  case "$host" in
    100.*) echo "malha Tailscale ($host)" ;;
    "")    echo "sem rota — o droplet não existe" ;;
    *)     echo "IP público ($host)" ;;
  esac
}

# Atalho de SSH com as opções que evitam travar em prompt interativo.
agent_ssh() {
  local ip; ip="$(agent_host)"
  [ -z "$ip" ] && { echo "🛑 droplet '$DROPLET_NAME' não existe" >&2; return 1; }
  ssh -i "$SSH_KEY_FILE" \
      -o StrictHostKeyChecking=accept-new \
      -o UserKnownHostsFile="$HOME/.ssh/known_hosts" \
      -o ConnectTimeout=10 \
      "agent@${ip}" "$@"
}

# root_ssh executa como root no droplet.
#
# É a autoridade do OPERADOR, e ela é a chave SSH que existe só no Mac.
#
# Existe porque operador e modelo compartilham o usuário `agent`: toda permissão
# dada ao operador por sudoers é dada ao modelo junto. Foi assim que uma regra
# de conveniência (`agent ALL=(agentd) agentd -catalog *`) desfez a proteção que
# impede o modelo de cadastrar conector apontando para onde quiser.
#
# Por aqui não há esse vazamento: o modelo não alcança esta chave por caminho
# nenhum. Usar para deploy, catálogo e cofre — nunca para diagnóstico, que deve
# rodar com o usuário restrito para exercitar as permissões de verdade.
root_ssh() {
  local ip; ip="$(agent_host)"
  [ -z "$ip" ] && { echo "🛑 droplet '$DROPLET_NAME' não existe" >&2; return 1; }
  ssh -i "$SSH_KEY_FILE" \
      -o StrictHostKeyChecking=accept-new \
      -o UserKnownHostsFile="$HOME/.ssh/known_hosts" \
      -o ConnectTimeout=10 \
      "root@${ip}" "$@"
}

# agentd_run roda o agentd como o usuário dono do cofre.
#
# Entra por root e desce para `agentd` — nunca por `agent`, que é o usuário do
# modelo. `setpriv` em vez de `sudo` porque não depende de regra em sudoers:
# a autoridade aqui já é root, e uma linha a menos no sudoers é uma linha a
# menos que o modelo poderia herdar.
agentd_run() {
  root_ssh "setpriv --reuid=agentd --regid=agentd --init-groups -- /usr/local/bin/agentd $*"
}
