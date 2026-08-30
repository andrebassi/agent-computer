#!/bin/bash
# Espera o cloud-init terminar, distinguindo os TRÊS estados que importam:
#   1. SSH ainda não responde        -> droplet bootando, seguir esperando
#   2. SSH responde, marca ausente   -> cloud-init rodando OU quebrado
#   3. cloud-init terminou com erro  -> abortar JÁ, não esperar 15 minutos
#
# A versão anterior colapsava 1 e 2 na mensagem "aguardando SSH responder" e
# nunca checava 3. Resultado medido: 12 minutos de espera e um diagnóstico
# errado, com o SSH funcionando o tempo todo e o cloud-init tendo recusado o
# YAML por mojibake nos primeiros 90 segundos.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

ip="$(droplet_ip)"
[ -z "$ip" ] && { echo "🛑 droplet nao existe — rodar 01-create.sh"; exit 1; }

ssh_root() {
  ssh -i "$SSH_KEY_FILE" -o StrictHostKeyChecking=accept-new \
      -o ConnectTimeout=8 -o BatchMode=yes "root@${ip}" "$@" 2>/dev/null
}

echo "esperando cloud-init em $ip (instalação leva ~5-8 min)"
deadline=$(( $(date +%s) + 900 ))

while [ "$(date +%s)" -lt "$deadline" ]; do
  # Estado 1: a porta SSH sequer autentica ainda.
  if ! ssh_root true; then
    echo "  [boot]     SSH ainda nao autentica ($(date +%H:%M:%S))"
    sleep 15
    continue
  fi

  ci_status="$(ssh_root 'cloud-init status 2>/dev/null | head -1')"

  # Estado 3: terminou, mas mal. Abortar agora — esperar não conserta.
  case "$ci_status" in
    *error*)
      echo
      echo "🛑 cloud-init terminou com ERRO. Nao adianta esperar."
      ssh_root 'cloud-init status --long 2>&1 | head -20'
      echo
      echo "   log completo: task logs"
      exit 1
      ;;
  esac

  # O YAML pode ter sido RECUSADO e o cloud-init ainda reportar "done":
  # os pacotes não instalam e nada avisa. Esta é a checagem que faltava.
  if ssh_root 'cloud-init status --long 2>&1' | grep -qi "Failed loading yaml"; then
    echo
    echo "🛑 cloud-init RECUSOU o user-data (YAML invalido) — nada foi instalado."
    ssh_root 'cloud-init status --long 2>&1 | grep -A3 -i "failed loading"'
    echo
    echo "   Causa tipica: mojibake por dupla codificacao UTF-8 no caminho"
    echo "   doctl -> API -> ConfigDrive. Conferir com:"
    echo "   ssh -i $SSH_KEY_FILE root@$ip 'head -c 200 /var/lib/cloud/instance/user-data.txt | cat -v'"
    exit 1
  fi

  # Estado final feliz.
  if [ "$(ssh_root 'cat /var/lib/agent-computer-ready 2>/dev/null')" = "READY" ]; then
    echo "✅ cloud-init concluido e marca presente"
    exit 0
  fi

  # Estado 2: rodando de verdade. Mostrar em que passo está, para a espera
  # ser informativa em vez de um relógio anônimo.
  passo="$(ssh_root 'tail -3 /var/log/cloud-init-output.log 2>/dev/null | tr -d "\r" | grep -v "^$" | tail -1' | cut -c1-70)"
  echo "  [ci:$ci_status] ${passo:-instalando...} ($(date +%H:%M:%S))"
  sleep 15
done

echo "🛑 estourou 15 min. Diagnostico: task logs"
exit 1
