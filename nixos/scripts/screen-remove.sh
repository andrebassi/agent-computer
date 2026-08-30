#!/bin/bash
# Derruba uma tela. O perfil do navegador dela CONTINUA em
# /workspace/browser/screen-N: derrubar a tela nao apaga a sessao,
# justamente para que ela volte igual em `screen-add`.
set -euo pipefail
N="${1:?uso: screen-remove <numero da tela, 2..9>}"
[[ "$N" =~ ^[2-9]$ ]] || { echo "a tela 1 nao se remove"; exit 1; }
sudo /run/current-system/sw/bin/systemctl disable --now "chrome@$N" "novnc@$N" "x11vnc@$N" "openbox@$N" "xvfb@$N"
echo "tela $N removida (perfil preservado em /workspace/browser/screen-$N)"
