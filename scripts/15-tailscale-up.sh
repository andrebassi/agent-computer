#!/bin/bash
# Coloca o computador do agente na malha Tailscale.
#
# Por que isso importa: hoje a tela só é alcançável por túnel SSH, o que amarra
# o acesso à chave privada. Na malha, o computador ganha um endereço estável e
# um nome, e o acesso passa a ser controlado pela ACL do tailnet — dá para
# compartilhar o nó com alguém sem entregar a chave da máquina.
#
# O IP público continua fechado: isto ACRESCENTA um caminho, não abre nenhum.
#
# ⚠️ O login é interativo por decisão, não por limitação: não há chave de
# autenticação no cofre, e criar uma exigiria um cliente OAuth do tailnet. O
# script vai até onde dá sozinho e entrega a URL para a pessoa clicar.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

ip="$(droplet_ip)"
[ -z "$ip" ] && { echo "🛑 droplet nao existe — rodar task up"; exit 1; }

hostname="${TAILSCALE_HOSTNAME:-agent-computer}"

echo "=== o cliente está instalado? ==="
if ! agent_ssh 'command -v tailscale >/dev/null'; then
  echo "  🛑 tailscale ausente — o cloud-init deveria ter instalado"
  echo "  → agent_ssh 'curl -fsSL https://tailscale.com/install.sh | sh'"
  exit 1
fi
echo "  ✅ $(agent_ssh 'tailscale version 2>/dev/null | head -1')"

echo
echo "=== já está na malha? ==="
backendState="$(agent_ssh 'tailscale status --json 2>/dev/null | python3 -c "
import sys,json
try:
    d=json.load(sys.stdin)
    print(d.get(\"BackendState\",\"?\"))
except Exception:
    print(\"SemEstado\")
"')"
if [ "$backendState" = "Running" ]; then
  echo "  já estava na malha:"
  agent_ssh 'tailscale ip -4 2>/dev/null | sed "s/^/     IP: /"'
  exit 0
fi
echo "  estado atual: ${backendState:-desconhecido}"

echo
echo "=== pedindo o link de autenticação ==="
# --ssh NÃO é ligado aqui de propósito: ele abriria acesso ao shell governado
# pela ACL do tailnet, o que é uma decisão de segurança separada de "entrar na
# malha". Quem quiser liga depois, sabendo o que está ligando.
#
# O comando é disparado em segundo plano porque ele fica pendurado esperando o
# login; o que interessa é a URL que ele imprime nos primeiros segundos.
agent_ssh "sudo nohup tailscale up --hostname='$hostname' --accept-dns=false \
  > /tmp/tailscale-up.log 2>&1 &" || true
sleep 6

url="$(agent_ssh 'grep -o "https://login.tailscale.com/[a-zA-Z0-9/]*" /tmp/tailscale-up.log 2>/dev/null | head -1')"
if [ -z "$url" ]; then
  echo "  🛑 nao consegui capturar a URL. Log da maquina:"
  agent_ssh 'cat /tmp/tailscale-up.log 2>/dev/null' | sed 's/^/     /'
  exit 1
fi

echo
echo "  ┌─────────────────────────────────────────────────────────────"
echo "  │ ABRA ESTE LINK PARA AUTORIZAR O COMPUTADOR NA SUA MALHA:"
echo "  │"
echo "  │   $url"
echo "  │"
echo "  │ Depois de autorizar, rode:  task tailscale-check"
echo "  └─────────────────────────────────────────────────────────────"
