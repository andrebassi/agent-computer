#!/bin/bash
# Tira uma imagem do DROPLET pronto, para a proxima criacao nascer feita.
#
# # O que isto economiza, medido
#
# Criar a maquina do zero custa ~15 min, e quase tudo e a conversao para NixOS:
# o `nixos-infect` baixa 614 MB de `cache.nixos.org`, constroi o sistema, reescreve
# o boot e reinicia. Com a imagem pronta, o droplet sobe direto no estado final.
#
# # O que isto NAO substitui
#
# O snapshot do VOLUME (`task snapshot`, script 04) e outra coisa e continua
# necessario: ali estao as tarefas, as conversas, o cofre e as habilidades. Este
# aqui guarda o SISTEMA -- pacotes, unidades, usuarios, o binario.
#
#   volume    o trabalho     muda toda hora     04-snapshot.sh
#   imagem    a maquina      muda por deploy    este script
#
# Restaurar a imagem sem o volume da uma maquina vazia e funcional; restaurar o
# volume sem a imagem da o trabalho de volta numa maquina que precisa ser
# montada. Os dois juntos sao a recuperacao completa.
#
# # O droplet e DESLIGADO antes
#
# Snapshot de disco em uso captura um sistema de arquivos no meio de uma escrita.
# O DigitalOcean permite fazer com a maquina ligada e AVISA que o resultado pode
# ficar inconsistente -- e um sistema de arquivos inconsistente numa imagem base
# e o pior tipo de defeito: ele so aparece na maquina que alguem criar depois,
# longe daqui.
#
# O custo e o tempo parado: o desligamento leva ~30s, o snapshot alguns minutos,
# e o religamento ~30s.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

STAMP="$(date +%Y%m%d-%H%M)"
IMAGE_NAME="${DROPLET_NAME}-image-${STAMP}"

id="$(droplet_id)"
if [ -z "$id" ]; then
  echo "🛑 droplet '$DROPLET_NAME' nao existe; nada a fotografar"
  exit 1
fi

echo "=== 1. estado antes ==="
# Conferir que a maquina esta SA antes de fotografa-la. Uma imagem tirada de um
# sistema com unidade em falha propaga o defeito para toda maquina criada dela,
# e o diagnostico vai parar muito longe daqui.
health="$(root_ssh 'systemctl is-system-running 2>&1; systemctl --failed --no-legend | head -3' 2>&1)"
echo "$health" | sed 's/^/  /'
case "$health" in
  running*) echo "  ✅ sistema saudavel" ;;
  degraded*)
    echo "  🛑 ha unidade em falha; conserte antes de fotografar"
    exit 1
    ;;
  *)
    echo "  ⚠️  estado inesperado; seguindo, mas confira o que veio acima"
    ;;
esac

echo
echo "=== 2. desligando (snapshot de disco em uso sai inconsistente) ==="
timeout 180s doctl compute droplet-action power-off "$id" --wait >/dev/null 2>&1

# O `--wait` volta quando a ACAO completa; o campo Status do droplet demora um
# instante a mais para refletir. Ler uma vez so devolve "active" com a maquina ja
# desligando -- e o script abortava dizendo que o desligamento falhou.
#
# E a mesma consistencia eventual que ja mordeu no destroy->create deste projeto.
# A espera e ATIVA, com teto: um droplet que de fato nao desliga precisa parar o
# script, nao prende-lo.
state=""
for _ in $(seq 1 20); do
  state="$(timeout 30s doctl compute droplet get "$id" --format Status --no-header 2>/dev/null | tr -d ' \r')"
  [ "$state" = "off" ] && break
  sleep 3
done
if [ "$state" != "off" ]; then
  echo "🛑 o droplet nao desligou em 60s (estado: ${state:-desconhecido}); abortando"
  exit 1
fi
echo "  ✅ desligado"

echo
echo "=== 3. tirando a imagem: $IMAGE_NAME ==="
# `--wait` e obrigatorio: sem ele o comando volta na hora e o religamento
# aconteceria no meio da leitura do disco.
if timeout 1200s doctl compute droplet-action snapshot "$id" \
     --snapshot-name "$IMAGE_NAME" --wait >/dev/null 2>&1; then
  echo "  ✅ imagem criada"
else
  echo "  🛑 falhou; religando o droplet antes de sair"
  timeout 120s doctl compute droplet-action power-on "$id" --wait >/dev/null 2>&1
  exit 1
fi

echo
echo "=== 4. religando ==="
timeout 180s doctl compute droplet-action power-on "$id" --wait >/dev/null 2>&1
state=""
for _ in $(seq 1 20); do
  state="$(timeout 30s doctl compute droplet get "$id" --format Status --no-header 2>/dev/null | tr -d ' \r')"
  [ "$state" = "active" ] && break
  sleep 3
done
[ "$state" = "active" ] && echo "  ✅ no ar" || echo "  ⚠️  estado: ${state:-desconhecido}"

echo
echo "=== 5. a imagem, e como usa-la ==="
snapshot_id="$(timeout 60s doctl compute snapshot list --resource droplet --no-header 2>/dev/null \
  | awk -v n="$IMAGE_NAME" '$2==n {print $1}' | head -1)"

if [ -z "$snapshot_id" ]; then
  echo "  🛑 a imagem nao aparece na listagem"
  exit 1
fi

size="$(timeout 60s doctl compute snapshot list --resource droplet --no-header 2>/dev/null \
  | awk -v n="$IMAGE_NAME" '$2==n {print $8, $9}')"
echo "  id:      $snapshot_id"
echo "  tamanho: ${size:-desconhecido}"
echo
echo "  Para criar a proxima maquina JA PRONTA:"
echo "    DROPLET_IMAGE=$snapshot_id task up"
echo
echo "  Isso pula a conversao para NixOS (~15 min) e o download de 614 MB do"
echo "  cache do Nix. O volume duravel entra como sempre, com o trabalho."

echo
echo "erros: 0"
