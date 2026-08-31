#!/usr/bin/env bash
#
# Provisiona o transporte escolhido por AGENT_NETWORK.
#
# Os quatro modos não custam o mesmo: `auto` e `ssh` não provisionam nada — o
# túnel SSH já funciona com a chave que o Mac tem. `tailscale` e `cloudflared`
# precisam de credencial na máquina, e é aí que mora a decisão que este script
# existe para tomar.
#
#   ./scripts/47-network-setup.sh              # usa $AGENT_NETWORK
#   AGENT_NETWORK=tailscale ./scripts/47-network-setup.sh
#
# 🛑 A CREDENCIAL NÃO VAI PELO cloud-init.
#
# O `user-data` do DigitalOcean é servido pelo metadata em 169.254.169.254, e o
# modelo tem shell irrestrito nesta máquina — `docs/SECURITY.md:129` já registra
# esse endereço como alcançável a partir da ferramenta de shell. Uma authkey ali
# seria legível por quem ela deveria conter: com ela o modelo adiciona nós ao
# tailnet pessoal do dono, que é escalada para FORA da máquina.
#
# Por isso a chave viaja por SSH de root depois do boot, como o binário do
# `agentd` já faz, e é apagada do disco assim que o `up` consome. O modelo nunca
# tem caminho até ela.

set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=scripts/lib.sh
source scripts/lib.sh
set +e

load_token

echo "=== transporte: $AGENT_NETWORK ==="

# setup_tailscale põe a máquina na malha usando uma authkey do cofre.
#
# A chave é `ephemeral` e `preauthorized` por decisão, não por conveniência:
# efêmera faz o nó sumir sozinho da lista quando o droplet é destruído — e este
# droplet é destruído o tempo todo, então sem isso o tailnet acumula fantasmas
# que disputam o mesmo hostname. Pré-autorizada evita que cada rebuild pare
# esperando aprovação no console.
setup_tailscale() {
  local authKey
  authKey="$(timeout 30s pass show bassi/tailscale/authkey 2>/dev/null | head -1)"

  # 🛑 O provisionamento NÃO pode ir pela malha — seria circular.
  #
  # `root_ssh` resolve o destino por `agent_host()`, e em `AGENT_NETWORK=tailscale`
  # essa função exige a malha e falha alto quando o nó não está nela. Mas o nó só
  # não está nela porque é exatamente isto que se vai consertar: para pôr a
  # máquina na malha é preciso alcançá-la primeiro, e o único caminho que não
  # depende do resultado é o IP público.
  #
  # Medido em 31/08/2026, na primeira execução real: o script morreu com
  # "o nó não está na malha" seguido de "droplet não existe" — a segunda
  # mensagem vinda do próprio SSH, que ficou sem destino. O erro descrevia o
  # problema que ele tentava resolver.
  #
  # `local` e não atribuição global: o bash tem escopo dinâmico, então
  # `root_ssh` chamada daqui enxerga este valor, e o `agent_route` do fim volta
  # a ver o modo que o dono pediu.
  local AGENT_NETWORK=ssh
  if [ -z "$authKey" ]; then
    echo "🛑 sem authkey em 'bassi/tailscale/authkey'"
    echo
    echo "   Criar em https://login.tailscale.com/admin/settings/keys com:"
    echo "     • Reusable      — o droplet é recriado a cada task up"
    echo "     • Ephemeral     — o nó some sozinho no destroy, sem deixar fantasma"
    echo "     • Preauthorized — senão cada rebuild espera aprovação no console"
    echo "     • Tag           — ex. tag:agent-computer, para a ACL não dar mais que o preciso"
    echo
    echo "   Depois: pass insert bassi/tailscale/authkey"
    return 1
  fi

  # A chave chega pela ENTRADA do processo remoto, não como argumento: argumento
  # aparece em `ps`, que o usuário `agent` — logo o modelo — consegue ler. É o
  # mesmo cuidado que `main.go` toma com o token do modelo.
  echo "  enviando a chave por SSH de root (nunca por cloud-init)..."
  printf '%s' "$authKey" | root_ssh "
    umask 077
    cat > /run/tailscale-authkey
    tailscale up --authkey=\"\$(cat /run/tailscale-authkey)\" \
      --hostname='${TAILSCALE_HOSTNAME}' --accept-dns=false
    result=\$?
    shred -u /run/tailscale-authkey 2>/dev/null || rm -f /run/tailscale-authkey
    exit \$result
  " || { echo "  🛑 o 'up' remoto falhou"; return 1; }

  # `--accept-dns=false` de propósito: a máquina resolve pelo DNS dela, e deixar
  # o Tailscale reescrever o resolvedor já quebrou a resolução do endpoint do
  # modelo em máquina de laboratório.

  echo "  confirmando que o nó entrou..."
  local state
  state="$(root_ssh "tailscale status --json 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get(\"BackendState\",\"?\"), (d.get(\"Self\") or {}).get(\"TailscaleIPs\",[\"\"])[0])'" 2>/dev/null)"
  echo "  estado na máquina: ${state:-desconhecido}"
  case "$state" in
    Running*) echo "  ✅ na malha" ;;
    *) echo "  ⚠️  o 'up' passou mas o estado não é Running — conferir com 'task ssh'"; return 1 ;;
  esac
}

# setup_cloudflared publica a máquina por um túnel da Cloudflare.
#
# Serve ao caso que a malha NÃO resolve: alguém que precisa alcançar a tela sem
# entrar no tailnet do dono. Em compensação o tráfego passa pela Cloudflare e o
# hostname é público, então o Access na frente não é opcional — sem ele o noVNC
# fica exposto a quem descobrir o nome.
setup_cloudflared() {
  if [ -z "$CLOUDFLARE_TUNNEL_HOSTNAME" ]; then
    echo "🛑 CLOUDFLARE_TUNNEL_HOSTNAME não definido"
    echo "   Não há padrão possível: um hostname adivinhado é a máquina de outra pessoa."
    return 1
  fi

  local tunnelToken
  tunnelToken="$(timeout 30s pass show bassi/cloudflare/tunnel-token 2>/dev/null | head -1)"
  if [ -z "$tunnelToken" ]; then
    echo "🛑 sem token em 'bassi/cloudflare/tunnel-token'"
    echo
    echo "   Criar o túnel em https://one.dash.cloudflare.com > Networks > Tunnels,"
    echo "   apontando '${CLOUDFLARE_TUNNEL_HOSTNAME}' para http://127.0.0.1:6081"
    echo "   (noVNC da tela 1), e pôr uma política de Access na frente."
    echo
    echo "   Depois: pass insert bassi/cloudflare/tunnel-token"
    return 1
  fi

  # Pelo mesmo motivo do tailscale: o hostname do túnel só resolve depois que o
  # túnel existe, então alcançar a máquina para criá-lo tem de ir pelo IP.
  local AGENT_NETWORK=ssh

  echo "  instalando o cloudflared e subindo o túnel..."
  # Aspas SIMPLES de propósito: o `$(cat ...)` tem de expandir na máquina, onde o
  # arquivo existe. Em aspas duplas ele expandiria aqui no Mac, onde não existe,
  # e o `service install` receberia string vazia sem reclamar.
  # shellcheck disable=SC2016
  printf '%s' "$tunnelToken" | root_ssh '
    umask 077
    cat > /run/cf-tunnel-token
    if ! command -v cloudflared >/dev/null 2>&1; then
      curl -fsSL https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \
        -o /usr/local/bin/cloudflared && chmod 0755 /usr/local/bin/cloudflared
    fi
    cloudflared service install "$(cat /run/cf-tunnel-token)"
    result=$?
    shred -u /run/cf-tunnel-token 2>/dev/null || rm -f /run/cf-tunnel-token
    exit $result
  ' || { echo "  🛑 a instalação remota falhou"; return 1; }

  echo "  ✅ túnel instalado — o hostname responde quando o DNS propagar"
}

case "$AGENT_NETWORK" in
  auto|ssh)
    echo "  nada a provisionar: o túnel SSH usa a chave que o Mac já tem."
    echo "  rota atual: $(agent_route)"
    ;;
  tailscale)   setup_tailscale ;;
  cloudflared) setup_cloudflared ;;
esac

status=$?
echo
echo "rota depois do provisionamento: $(agent_route 2>&1 | head -1)"
exit $status
