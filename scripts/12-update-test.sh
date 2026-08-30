#!/bin/bash
# Prova o verbo UPDATE da doc: reconstruir o computador com imagem nova
# PRESERVANDO o estado duravel, e descartando o efemero.
#
# E o teste que a arquitetura anterior nao tinha como passar: com /workspace no
# disco do droplet, destruir o droplet levava o trabalho junto. So faz sentido
# depois que o durave passou a morar num volume separado.
#
# Grava marcas nos dois lados da fronteira e confere, depois do rebuild, que
# cada uma teve o destino certo: /workspace sobrevive, /scratch nao.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token
errs=0

ip_antes="$(droplet_ip)"
id_antes="$(droplet_id)"
[ -z "$id_antes" ] && { echo "droplet nao existe"; exit 1; }

marca="update-test-$(date +%s)"
echo "=== ANTES: gravando marcas nos dois lados da fronteira ==="
agent_ssh "echo '$marca' > /workspace/DURAVEL.txt && echo '$marca' > /scratch/EFEMERO.txt"
agent_ssh 'mkdir -p /workspace/projects/exemplo && echo conteudo > /workspace/projects/exemplo/arquivo.txt'
# Pacote instalado na mao: a doc classifica explicitamente como substituivel.
agent_ssh 'sudo apt-get install -y -qq cowsay >/dev/null 2>&1' || true
echo "  /workspace/DURAVEL.txt   : $(agent_ssh 'cat /workspace/DURAVEL.txt')"
echo "  /scratch/EFEMERO.txt     : $(agent_ssh 'cat /scratch/EFEMERO.txt')"
echo "  cowsay (instalado na mao): $(agent_ssh 'which cowsay || echo ausente')"
echo "  perfil do navegador      : $(agent_ssh 'du -sh /workspace/browser 2>/dev/null | cut -f1')"
echo "  droplet                  : $id_antes ($ip_antes)"

echo
echo "=== UPDATE ==="
echo UPDATE | "$(dirname "$0")/10-update.sh" || { echo "🛑 update falhou"; exit 1; }

id_depois="$(droplet_id)"
ip_depois="$(droplet_ip)"
echo
echo "=== DEPOIS ==="
echo "  droplet: $id_depois ($ip_depois)"
if [ "$id_depois" != "$id_antes" ]; then
  echo "  ✅ droplet foi de fato reconstruido (id mudou)"
else
  echo "  🛑 droplet NAO foi reconstruido"; errs=$((errs+1))
fi

echo
echo "--- o que DEVE ter sobrevivido ---"
v="$(agent_ssh 'cat /workspace/DURAVEL.txt 2>/dev/null')"
[ "$v" = "$marca" ] && echo "  ✅ /workspace/DURAVEL.txt preservado" || { echo "  🛑 /workspace/DURAVEL.txt perdido"; errs=$((errs+1)); }
v="$(agent_ssh 'cat /workspace/projects/exemplo/arquivo.txt 2>/dev/null')"
[ "$v" = "conteudo" ] && echo "  ✅ projeto em /workspace preservado" || { echo "  🛑 projeto perdido"; errs=$((errs+1)); }
v="$(agent_ssh 'test -d /workspace/browser/screen-1/Default && echo sim')"
[ "$v" = "sim" ] && echo "  ✅ perfil do navegador preservado" || { echo "  🛑 perfil do navegador perdido"; errs=$((errs+1)); }

echo
echo "--- o que DEVE ter sido descartado ---"
v="$(agent_ssh 'cat /scratch/EFEMERO.txt 2>/dev/null')"
[ -z "$v" ] && echo "  ✅ /scratch descartado, como a doc manda" || { echo "  🛑 /scratch sobreviveu (fronteira nao esta valendo)"; errs=$((errs+1)); }
v="$(agent_ssh 'which cowsay 2>/dev/null')"
[ -z "$v" ] && echo "  ✅ pacote instalado na mao descartado" || { echo "  🛑 cowsay sobreviveu"; errs=$((errs+1)); }

echo
echo "--- a tela voltou? ---"
for unit in xvfb openbox x11vnc novnc chrome; do
  st="$(agent_ssh "systemctl is-active ${unit}@1.service 2>/dev/null")"
  [ "$st" = "active" ] && echo "  ✅ ${unit}@1" || { echo "  🛑 ${unit}@1: ${st:-sem resposta}"; errs=$((errs+1)); }
done

echo
echo "erros: $errs"
exit $errs
