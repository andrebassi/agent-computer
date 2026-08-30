#!/bin/bash
# Propaga a sessao do navegador de uma tela para outra.
#
# Resolve o que a semeadura do screen-add nao alcanca: um login feito na
# tela de origem DEPOIS de a tela de destino ja existir.
#
# Uso: session-sync <origem> <destino>     por exemplo: session-sync 1 2
#
# A copia e feita com o Chrome do DESTINO parado, e nao do origem. Copiar
# de um perfil em uso pega o SQLite de cookies a meio de uma escrita, e o
# sintoma e um perfil que abre "deslogado" sem erro nenhum -- o pior tipo
# de falha, porque parece que a sessao expirou.
#
# Mesmo assim o resultado nao e atomico: se alguem estiver navegando na
# origem durante a copia, a foto sai de instantes diferentes. Para sessao
# de login isso basta, porque o cookie ja estava gravado antes.
set -euo pipefail
FROM="${1:?uso: session-sync <tela de origem> <tela de destino>}"
TO="${2:?uso: session-sync <tela de origem> <tela de destino>}"
[[ "$FROM" =~ ^[1-9]$ && "$TO" =~ ^[1-9]$ ]] || { echo "telas de 1 a 9"; exit 1; }
[ "$FROM" = "$TO" ] && { echo "origem e destino sao a mesma tela"; exit 1; }

SRC="/workspace/browser/screen-$FROM"
DST="/workspace/browser/screen-$TO"
[ -d "$SRC" ] || { echo "a tela $FROM nao tem perfil em $SRC"; exit 1; }

echo "parando o navegador da tela $TO"
sudo /run/current-system/sw/bin/systemctl stop "chrome@$TO" 2>/dev/null || true
sleep 2

echo "copiando a sessao da tela $FROM para a tela $TO"
# O historico do destino e preservado de proposito: o que se quer propagar
# e a SESSAO (cookies, logins, tokens), nao o que o outro agente visitou.
rm -rf "$DST.anterior"
[ -d "$DST" ] && mv "$DST" "$DST.anterior"
mkdir -p "$DST"
cp -a --reflink=auto "$SRC/." "$DST/" 2>/dev/null || cp -a "$SRC/." "$DST/"
rm -f "$DST"/Singleton* "$DST"/*.lock 2>/dev/null || true
# `chown` SEM sudo, de proposito.
#
# Ele estava com `sudo`, herdado de quando o script rodava como root -- e desde
# que a lista de sudo virou fechada (chown ficou de fora: e root por outro
# caminho, bastaria apontar para o arquivo de senha do cofre) esta linha
# ABORTAVA o script inteiro, com `set -e`, no meio de uma copia de perfil.
#
# Nao precisa de privilegio nenhum: o script roda como `agent` e o destino ja e
# `agent:agent`. Medido em 30/08/2026.
chown -R agent:agent "$DST"

echo "religando o navegador da tela $TO"
sudo /run/current-system/sw/bin/systemctl start "chrome@$TO"
sleep 3
if [ "$(systemctl is-active "chrome@$TO")" = "active" ]; then
  echo "OK. O perfil anterior ficou em $DST.anterior -- apague quando conferir."
else
  echo "FALHOU ao religar. Restaurando o perfil anterior."
  rm -rf "$DST"
  mv "$DST.anterior" "$DST"
  sudo /run/current-system/sw/bin/systemctl start "chrome@$TO"
  exit 1
fi
