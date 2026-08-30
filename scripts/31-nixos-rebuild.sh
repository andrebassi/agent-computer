#!/bin/bash
# Aplica uma mudanca de nixos/host.nix na maquina, SEM recria-la.
#
# # Por que existe
#
# Trocar uma linha da configuracao nao pode custar uma reconstrucao inteira. Pelo
# caminho de criacao, toda edicao significaria: destruir, criar, esperar a
# conversao baixar o nixpkgs e construir o sistema, reiniciar -- cerca de 15
# minutos. Por aqui e um `nixos-rebuild switch`, que reaproveita tudo o que ja
# esta no store: segundos.
#
# # A propriedade que ele preserva
#
# `switch` aplica a configuracao nova E cria uma GERACAO. Se algo quebrar, o
# rollback e `nixos-rebuild switch --rollback` -- a maquina volta ao estado
# anterior sem passar por reconstrucao. E o ganho do caminho declarativo que o
# cloud-init nao tem: la, desfazer e recriar.
#
# # Por que por SSH de root
#
# Mesma razao do deploy do binario: e a autoridade do OPERADOR, e a chave existe
# so no Mac. O modelo roda como `agent` e nao alcanca este caminho -- se
# alcancasse, poderia reescrever a propria configuracao do sistema, incluindo o
# rebaixamento que o mantem longe do cofre.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
# Sem isto, `droplet_ip` nao consulta a API, o `ssh` devolve rc=1 e o `set -e`
# aborta o script SEM MENSAGEM -- ele parece ter terminado na etapa 1.
load_token

repoRoot="$(cd "$(dirname "$0")/.." && pwd)"

echo "1/5 conferindo que a maquina e NixOS"
osName="$(agent_os)"
if [ "$osName" != "nixos" ]; then
  echo "🛑 a maquina no ar e '$osName', nao NixOS."
  echo "   Este script so serve ao caminho declarativo. Para trocar:"
  echo "     task destroy && AGENT_OS=nixos task up"
  exit 1
fi
ok_ip="$(droplet_ip)"
echo "  NixOS em $ok_ip"

echo
echo "2/5 verificando a configuracao ANTES de enviar"
# O mesmo verificador que roda antes de gastar droplet. Enviar uma configuracao
# que nao avalia deixaria a maquina no estado anterior -- o `switch` recusa --
# mas gastaria a viagem e a mensagem viria do outro lado, mais longe da causa.
"$repoRoot/scripts/30-nixos-validate.sh" > /tmp/nixos-rebuild-check.log 2>&1 || {
  echo "🛑 a configuracao nao passa na verificacao local; nada foi enviado:"
  tail -12 /tmp/nixos-rebuild-check.log | sed 's/^/    /'
  exit 1
}
echo "  configuracao valida"

echo
echo "3/5 enviando o modulo e os auxiliares"
host="$(agent_host)"
sshOpts=(-i "$SSH_KEY_FILE" -o StrictHostKeyChecking=accept-new
         -o UserKnownHostsFile="$HOME/.ssh/known_hosts")
timeout 60s ssh "${sshOpts[@]}" "root@${host}" 'mkdir -p /etc/nixos/scripts'
timeout 120s scp "${sshOpts[@]}" \
  "$repoRoot/nixos/host.nix" "$repoRoot/nixos/agent-authorized-keys" \
  "root@${host}:/etc/nixos/"
timeout 120s scp "${sshOpts[@]}" \
  "$repoRoot"/nixos/scripts/*.sh "root@${host}:/etc/nixos/scripts/"

echo
echo "4/5 aplicando (nixos-rebuild switch)"
# `--fast` pula a reconstrucao do proprio nixos-rebuild, que nao muda aqui.
# A saida vai para um arquivo na maquina porque, quando um servico falha ao
# subir, e nela que esta o motivo -- e o `switch` ja terminou quando isso
# aparece.
timeout 900s ssh "${sshOpts[@]}" "root@${host}" \
  'nixos-rebuild switch --fast 2>&1 | tee /tmp/nixos-rebuild.log | tail -25'

echo
echo "5/5 conferindo pelo EFEITO, nao pelo codigo de saida"
# `switch` sai 0 mesmo quando um servico falha a subir depois: ele reporta o
# sucesso da ATIVACAO, nao a saude do que foi ativado. Conferir as unidades e o
# que separa "aplicou" de "funciona".
timeout 60s ssh "${sshOpts[@]}" "root@${host}" '
  echo "  geracao atual: $(readlink -f /run/current-system | sed "s|/nix/store/||")"
  echo "  unidades falhadas:"
  systemctl --failed --no-legend --no-pager | sed "s/^/    /" || true
  systemctl is-system-running 2>/dev/null | sed "s/^/  estado do sistema: /"
'
echo
echo "rollback, se precisar: ssh root@${host} nixos-rebuild switch --rollback"
