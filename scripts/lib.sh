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
# Aceita também o ID de um SNAPSHOT DE DROPLET, tirado por
# `scripts/38-image-snapshot.sh`. É como se pula a conversão para NixOS:
#
#   DROPLET_IMAGE=<id-do-snapshot> task up
#
# A máquina nasce no estado final — sistema, unidades, usuários e binário já
# prontos —, em vez de nascer Ubuntu e levar ~15 min sendo convertida, com 614 MB
# baixados do cache do Nix.
#
# ⚠️ A imagem guarda o SISTEMA; o volume guarda o TRABALHO. São duas fotos
# diferentes e as duas fazem falta: a imagem sem o volume dá uma máquina vazia e
# funcional, o volume sem a imagem dá o trabalho de volta numa máquina que ainda
# precisa ser montada.
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

# QUATRO caminhos de rede, escolhidos por AGENT_NETWORK:
#
#   auto        tenta a malha, cai para o IP público sem reclamar — o padrão
#   tailscale   EXIGE a malha; falha alto se o nó não estiver nela
#   ssh         força o IP público, ignorando a malha mesmo que ela esteja no ar
#   cloudflared força o hostname do túnel Cloudflare
#
# O padrão é `auto` porque é o comportamento que já existia, e trocá-lo por um
# dos modos exigentes quebraria toda máquina em uso. Mas `auto` tem um custo que
# só aparece quando algo falha: a queda para o IP público é SILENCIOSA, então
# uma malha caída se parece com uma malha funcionando, e o diagnóstico procura
# defeito onde não há.
#
# É para isso que servem os modos declarados: `AGENT_NETWORK=tailscale` não cai
# para lugar nenhum — ou vai pela malha, ou diz que não dá. Quem quer o
# transporte estável quer saber quando ele não está lá.
AGENT_NETWORK="${AGENT_NETWORK:-auto}"
case "$AGENT_NETWORK" in
  auto|tailscale|ssh|cloudflared) ;;
  *) echo "🛑 AGENT_NETWORK inválido: '$AGENT_NETWORK' (use 'auto', 'tailscale', 'ssh' ou 'cloudflared')" >&2; return 1 2>/dev/null || exit 1 ;;
esac

# Hostname do túnel Cloudflare, usado quando AGENT_NETWORK=cloudflared.
#
# Sem padrão de propósito: um hostname inventado resolveria para a máquina de
# outra pessoa, e a conexão tentaria autenticar lá. Vazio falha alto, que é o
# comportamento certo para um endereço que ninguém pode adivinhar.
CLOUDFLARE_TUNNEL_HOSTNAME="${CLOUDFLARE_TUNNEL_HOSTNAME:-}"

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
# Cache do IP dentro de UMA execução.
#
# `agent_ssh` chama isto a cada comando, e uma suíte faz dezenas de chamadas --
# ou seja, dezenas de consultas à API do DigitalOcean para um dado que não muda
# durante a execução. Um soluço da API no meio de um teste vira falso vermelho:
# `droplet_ip` devolve vazio, o SSH tenta conectar a lugar nenhum e o log diz
# "droplet 'agent-computer' não existe" com a máquina de pé.
#
# Medido em 30/08/2026, no `13-integration-test`: três seções reprovaram por
# isso, com `uptime` respondendo normalmente meio minuto depois.
#
# O cache dura só o processo. Recriar o droplet num script que já resolveu o IP
# exigiria limpar a variável -- e nenhum script faz as duas coisas, porque
# criação e verificação são scripts diferentes.
DROPLET_IP_CACHE="${DROPLET_IP_CACHE:-}"

droplet_ip() {
  if [ -n "$DROPLET_IP_CACHE" ]; then
    printf '%s\n' "$DROPLET_IP_CACHE"
    return 0
  fi
  local address
  # Duas tentativas: a API do DO tem soluço, e a segunda leitura custa 3s
  # contra o custo de reprovar uma suíte inteira de vinte minutos.
  for _ in 1 2; do
    address="$(timeout 30s doctl compute droplet list --format Name,PublicIPv4 --no-header 2>/dev/null \
      | awk -v n="$DROPLET_NAME" '$1==n {print $2}' | head -1)"
    [ -n "$address" ] && break
    sleep 3
  done
  [ -n "$address" ] && DROPLET_IP_CACHE="$address"
  printf '%s\n' "$address"
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
#
# ⚠️ A reserva silenciosa vale só em `AGENT_NETWORK=auto`. Nos modos declarados
# não há queda: o ponto deles é justamente falhar quando o transporte pedido não
# está lá, em vez de entregar outro parecendo o certo.
#
# mesh_address devolve o endereço do computador na malha, ou vazio.
#
# Separada de `agent_host` porque as duas respondem perguntas diferentes: esta
# diz se a malha alcança o nó, aquela decide por onde ir. Juntas, um modo que
# exige a malha não teria como distinguir "não está na malha" de "caiu para o IP
# público" — que é exatamente a confusão que AGENT_NETWORK existe para desfazer.
mesh_address() {
  command -v tailscale >/dev/null 2>&1 || return 0
  # `tailscale status` sai com código diferente de zero quando o serviço está
  # parado, e é assim que se distingue "malha caída" de "nó offline".
  timeout 8s tailscale status --json 2>/dev/null | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
except Exception:
    raise SystemExit
if data.get('BackendState') != 'Running':
    raise SystemExit
for peer in (data.get('Peer') or {}).values():
    peerName = (peer.get('HostName') or '').split('.')[0]
    if peerName == '$TAILSCALE_HOSTNAME' and peer.get('Online'):
        addresses = peer.get('TailscaleIPs') or []
        if addresses:
            print(addresses[0])
        break
" 2>/dev/null
}

agent_host() {
  local meshAddress
  case "$AGENT_NETWORK" in
    ssh)
      # Força o IP público mesmo com a malha no ar. É o modo de diagnosticar a
      # própria malha: sem ele, não há como provar que um defeito é dela.
      droplet_ip
      ;;
    tailscale)
      meshAddress="$(mesh_address)"
      if [ -z "$meshAddress" ]; then
        echo "🛑 AGENT_NETWORK=tailscale, mas o nó '$TAILSCALE_HOSTNAME' não está na malha." >&2
        echo "   Estado local: $(timeout 8s tailscale status --json 2>/dev/null | python3 -c 'import sys,json; print(json.load(sys.stdin).get("BackendState","?"))' 2>/dev/null || echo 'tailscale indisponível')" >&2
        echo "   Sem queda para o IP público: o modo declarado existe para não esconder isto." >&2
        return 1
      fi
      printf '%s\n' "$meshAddress"
      ;;
    cloudflared)
      if [ -z "$CLOUDFLARE_TUNNEL_HOSTNAME" ]; then
        echo "🛑 AGENT_NETWORK=cloudflared exige CLOUDFLARE_TUNNEL_HOSTNAME." >&2
        echo "   Não há padrão possível: um hostname adivinhado é a máquina de outra pessoa." >&2
        return 1
      fi
      printf '%s\n' "$CLOUDFLARE_TUNNEL_HOSTNAME"
      ;;
    *)
      # auto — o comportamento histórico, com reserva silenciosa.
      meshAddress="$(mesh_address)"
      if [ -n "$meshAddress" ]; then
        printf '%s\n' "$meshAddress"
        return 0
      fi
      droplet_ip
      ;;
  esac
}

# Diz por qual caminho o acesso está indo, para o diagnóstico não virar
# adivinhação quando algo falhar.
#
# O modo entra na resposta porque o endereço sozinho não distingue "escolhi a
# malha" de "caí nela": em `auto` os dois produzem o mesmo `100.x`, e é a
# diferença que importa quando algo falha.
agent_route() {
  local host
  if ! host="$(agent_host)"; then
    echo "sem rota — AGENT_NETWORK=$AGENT_NETWORK não está disponível"
    return 1
  fi
  case "$host" in
    100.*) echo "malha Tailscale ($host, modo $AGENT_NETWORK)" ;;
    "")    echo "sem rota — o droplet não existe" ;;
    *.*.*[a-z]*) echo "túnel Cloudflare ($host, modo $AGENT_NETWORK)" ;;
    *)     echo "IP público ($host, modo $AGENT_NETWORK)" ;;
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
