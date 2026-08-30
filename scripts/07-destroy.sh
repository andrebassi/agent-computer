#!/bin/bash
# Destrói o droplet. Exige confirmação digitada — é irreversível e leva junto
# tudo que estiver em /workspace e não tiver ido para um snapshot.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

id="$(droplet_id)"
[ -z "$id" ] && { echo "droplet nao existe — nada a destruir."; exit 0; }

echo "vai destruir o droplet '$DROPLET_NAME' (id $id, IP $(droplet_ip))."
echo
echo "    PRESERVA: /workspace, perfil do navegador e sessões logadas."
echo "              Elas moram no volume '$VOLUME_NAME', que NÃO é tocado aqui."
echo "              'task up' remonta tudo como estava."
echo
echo "    PERDE   : /scratch, pacotes instalados à mão, o IP público,"
echo "              e qualquer processo em execução."
echo
echo "    Custo depois: só o volume, US\$ 2,00/mês em vez de US\$ 26,00."
read -r -p "    digitar DESTROY para confirmar: " ok
[ "$ok" = "DESTROY" ] || { echo "cancelado"; exit 1; }

timeout 120s doctl compute droplet delete "$id" --force
echo "✅ droplet destruído. O estado durável continua no volume:"
timeout 30s doctl compute volume list --format Name,Size,Region 2>/dev/null | grep -E "Name|$VOLUME_NAME" || true
echo
echo "snapshots do volume (US\$ 0,06/GB/mês):"
timeout 30s doctl compute snapshot list --resource volume --format Name,Size,CreatedAt 2>/dev/null | grep -E "Name|$VOLUME_NAME" || echo "  nenhum"
echo
echo "para voltar:  task up"
