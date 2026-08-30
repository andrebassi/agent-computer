#!/bin/bash
# UPDATE (verbo da doc): reconstroi o computador com imagem nova, PRESERVANDO
# o estado duravel.
#
# Isto so e possivel porque /workspace mora num volume de bloco separado. O
# droplet e deliberadamente descartavel: destruir e recriar troca o sistema
# operacional, os pacotes e qualquer sujeira acumulada, e o volume volta a ser
# montado com /workspace, o perfil do navegador e as sessoes intactos.
#
# O que se perde, e a doc diz que deve se perder: /scratch, pacote instalado
# na mao e estado de aplicacao nao commitado.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

id="$(droplet_id)"
[ -z "$id" ] && { echo "droplet nao existe; use 01-create.sh"; exit 1; }

vol_id="$(timeout 30s doctl compute volume list --format ID,Name --no-header \
  | awk '$2=="agent-computer-workspace"{print $1}')"
[ -z "$vol_id" ] && { echo "🛑 volume duravel nao existe — abortando: um update sem ele APAGARIA o trabalho"; exit 1; }

echo "UPDATE do agent computer"
echo "  droplet atual : $id (sera destruido)"
echo "  volume duravel: $vol_id (sera preservado e remontado)"
echo
echo "  Preserva : /workspace, perfil do navegador, sessoes"
echo "  Descarta : /scratch, pacotes instalados na mao, estado nao commitado"
echo
read -r -p "  digitar UPDATE para confirmar: " ok
[ "$ok" = "UPDATE" ] || { echo "cancelado"; exit 1; }

echo
echo "1/4 parando servicos para desmontar o volume limpo"
agent_ssh 'sudo systemctl stop "chrome@*" "novnc@*" "x11vnc@*" "openbox@*" "xvfb@*" 2>/dev/null; sync' || true

echo "2/4 destacando volume"
timeout 180s doctl compute volume-action detach "$vol_id" "$id" --wait

echo "3/4 destruindo droplet antigo"
timeout 120s doctl compute droplet delete "$id" --force

echo "4/4 criando droplet novo com a imagem mais recente"
"$(dirname "$0")/01-create.sh"
"$(dirname "$0")/02-wait-ready.sh"

echo
echo "conferindo que o estado duravel voltou:"
agent_ssh 'ls -la /workspace/ 2>/dev/null | head; echo; cat /var/lib/agent-computer-volume'
