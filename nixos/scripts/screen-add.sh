#!/bin/bash
# Cria mais uma tela na MESMA maquina, no modelo da doc: um computador
# por conta, uma tela por agente. As telas sao superficies de trabalho
# separadas, NAO fronteiras de seguranca -- quem alcanca uma alcanca o
# mesmo /workspace e as mesmas credenciais de linha de comando.
# Uso: screen-add 2
set -euo pipefail
N="${1:?uso: screen-add <numero da tela, 2..9>}"
[[ "$N" =~ ^[2-9]$ ]] || { echo "tela deve ser de 2 a 9 (a 1 sobe no boot)"; exit 1; }

# SEMEADURA DE SESSAO.
#
# A documentacao diz que logar num site por um agente deixa a sessao
# disponivel para os outros. O Chrome nao permite dois processos no mesmo
# user-data-dir -- ha um SingletonLock -- entao a sessao nao pode ser
# literalmente compartilhada. O que da e SEMEAR: a tela nova nasce com uma
# copia do perfil da tela 1.
#
# Isto funciona porque o Chrome sobe com --password-store=basic, e nesse
# modo os cookies sao cifrados com chave fixa, nao com o chaveiro do
# usuario. Com o chaveiro, a copia produziria um perfil cujos cookies nao
# descriptografam, e o sintoma seria "deslogado" sem erro nenhum.
#
# LIMITE, e ele e real: login feito na tela 1 DEPOIS de a tela N existir
# nao se propaga sozinho. Para isso ha o session-sync.
SEED=/workspace/browser/screen-1
TARGET="/workspace/browser/screen-$N"
if [ ! -d "$TARGET" ] && [ -d "$SEED" ]; then
  echo "semeando a tela $N com a sessao da tela 1"
  mkdir -p "$TARGET"
  # --reflink=auto usa copia por referencia quando o sistema de arquivos
  # suporta; num perfil de centenas de megabytes isso e a diferenca entre
  # instantaneo e meio minuto.
  cp -a --reflink=auto "$SEED/." "$TARGET/" 2>/dev/null || cp -a "$SEED/." "$TARGET/"
  # As travas do processo de origem nao podem viajar junto: elas apontam
  # para um PID que nao e o do Chrome novo, e o Chrome recusa subir.
  rm -f "$TARGET"/Singleton* "$TARGET"/*.lock 2>/dev/null || true
  chown -R agent:agent "$TARGET"
fi

# MARCADOR NO VOLUME, e nao `systemctl enable`.
#
# `enable` grava um symlink em /etc/systemd/system -- que no NixOS e
# READ-ONLY (aponta para o store). O comando falha com:
#
#   Failed to enable unit: File /etc/systemd/system/xvfb@2.service:
#   Read-only file system
#
# Medido em 30/08/2026: `screen-add 2` saia com rc=1 e NENHUMA unidade da
# tela subia. O teste integrado reprovou em "tela 2 nao subiu" -- e a
# tarefa seguinte, que nao precisava de navegador, concluiu normalmente,
# o que deixava o log com cara de contradicao.
#
# O marcador resolve os dois sistemas com o mesmo mecanismo, e poe a fonte
# de verdade onde ela ja deveria estar: no volume duravel. Quem sobe as
# telas no boot e o `agent-screens.service`, que le este diretorio.
mkdir -p /workspace/agent/screens
: > "/workspace/agent/screens/$N"

sudo /run/current-system/sw/bin/systemctl start "xvfb@$N" "openbox@$N" "x11vnc@$N" "novnc@$N" "chrome@$N"
sleep 3

# Conferir o EFEITO, e nao o codigo de saida do `start`: `systemctl start`
# devolve 0 assim que a unidade e aceita, antes de ela ficar de pe. Uma
# unidade que sobe e morre em seguida deixaria este script dizendo "no ar".
failed=""
for unit in "xvfb@$N" "openbox@$N" "x11vnc@$N" "novnc@$N" "chrome@$N"; do
  state="$(systemctl is-active "$unit.service" 2>/dev/null || true)"
  [ "$state" = "active" ] || failed="$failed $unit($state)"
done
if [ -n "$failed" ]; then
  echo "ERRO: a tela $N NAO subiu inteira; fora do ar:$failed" >&2
  exit 1
fi

echo "tela $N no ar:"
echo "  VNC  127.0.0.1:$((5900 + N))"
echo "  web  127.0.0.1:$((6080 + N))"
echo "  CDP  127.0.0.1:922$N"
