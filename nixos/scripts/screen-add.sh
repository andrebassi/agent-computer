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
SEMENTE=/workspace/browser/screen-1
DESTINO="/workspace/browser/screen-$N"
if [ ! -d "$DESTINO" ] && [ -d "$SEMENTE" ]; then
  echo "semeando a tela $N com a sessao da tela 1"
  mkdir -p "$DESTINO"
  # --reflink=auto usa copia por referencia quando o sistema de arquivos
  # suporta; num perfil de centenas de megabytes isso e a diferenca entre
  # instantaneo e meio minuto.
  cp -a --reflink=auto "$SEMENTE/." "$DESTINO/" 2>/dev/null || cp -a "$SEMENTE/." "$DESTINO/"
  # As travas do processo de origem nao podem viajar junto: elas apontam
  # para um PID que nao e o do Chrome novo, e o Chrome recusa subir.
  rm -f "$DESTINO"/Singleton* "$DESTINO"/*.lock 2>/dev/null || true
  chown -R agent:agent "$DESTINO"
fi

sudo /run/current-system/sw/bin/systemctl enable --now "xvfb@$N" "openbox@$N" "x11vnc@$N" "novnc@$N" "chrome@$N"
sleep 3
echo "tela $N no ar:"
echo "  VNC  127.0.0.1:$((5900 + N))"
echo "  web  127.0.0.1:$((6080 + N))"
echo "  CDP  127.0.0.1:922$N"
