#!/bin/bash
# Derruba uma tela. O perfil do navegador dela CONTINUA em
# /workspace/browser/screen-N: derrubar a tela nao apaga a sessao,
# justamente para que ela volte igual em `screen-add`.
set -euo pipefail
N="${1:?uso: screen-remove <numero da tela, 2..9>}"
[[ "$N" =~ ^[2-9]$ ]] || { echo "a tela 1 nao se remove"; exit 1; }

# O marcador sai ANTES de parar as unidades: se o comando morrer no meio, a
# tela fica parada e nao volta no proximo boot -- que e o estado que a pessoa
# pediu. Na ordem inversa, uma falha deixaria a tela parada agora e de pe no
# boot seguinte, sem ninguem entender por que ela "voltou sozinha".
rm -f "/workspace/agent/screens/$N"

# `stop`, e nao `disable --now`: no NixOS /etc/systemd/system e read-only e o
# `disable` falha. Quem decide o que sobe no boot e o marcador acima, lido pelo
# agent-screens.service.
sudo /run/current-system/sw/bin/systemctl stop "chrome@$N" "novnc@$N" "x11vnc@$N" "openbox@$N" "xvfb@$N"
echo "tela $N removida (perfil preservado em /workspace/browser/screen-$N)"
