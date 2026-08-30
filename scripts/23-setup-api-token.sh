#!/bin/bash
# Gera o token da porta HTTP, guarda no pass e instala no droplet.
#
# A porta falha FECHADA sem este arquivo: uma porta que sobe sem autenticacao
# porque o token sumiu e o pior desfecho possivel — tudo funciona, ninguem
# percebe, e a maquina fica aberta com acesso a shell, navegador e credenciais.
#
# O VALOR NUNCA E IMPRESSO. Ele nasce no `openssl`, vai para o pass e para o
# droplet por stdin, e o arquivo temporario morre em `shred`. Um `echo` aqui
# deixaria a credencial no log, no scrollback e no historico do terminal.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

passEntry="bassi/agent-computer/api-token"
remoteFile="/workspace/agent/api-token"

# umask ANTES de qualquer escrita: criar o arquivo e corrigir a permissao depois
# deixa uma janela em que ele existe legivel por todos.
umask 077
tokenFile="$(mktemp "${TMPDIR:-/tmp}/api-token.XXXXXX")"
cleanup() { command -v shred >/dev/null 2>&1 && shred -u "$tokenFile" 2>/dev/null || rm -f "$tokenFile"; }
trap cleanup EXIT

echo "1/4 gerando token"
# 32 bytes em hexadecimal = 64 caracteres, bem acima do minimo de 32 que o
# servidor exige. Valor gerado, nunca digitado: token digitado a mao e
# adivinhavel.
openssl rand -hex 32 > "$tokenFile"
echo "  gerado: $(wc -c < "$tokenFile" | tr -d ' ') bytes"

echo
echo "2/4 guardando no pass em $passEntry"
pass insert -m -f "$passEntry" < "$tokenFile" >/dev/null
echo "  gravado"

echo
echo "3/4 instalando no droplet"
# `cat >` com umask no destino, e nao scp: o scp cria o arquivo com a permissao
# do original e so depois seria corrigido, deixando a mesma janela.
agent_ssh "umask 077 && cat > $remoteFile" < "$tokenFile"
agent_ssh "chmod 600 $remoteFile && stat -c 'permissao %a, %s bytes' $remoteFile"

echo
echo "4/4 conferindo pelo EFEITO: a porta aceita o token?"
# Se o servico ja estiver no ar, prova que o token instalado e o que ele aceita.
# Se nao estiver, diz isso em vez de fingir sucesso.
if agent_ssh "systemctl is-active --quiet agentd-api" 2>/dev/null; then
  agent_ssh "curl -sS --max-time 5 -o /dev/null -w 'POST autenticado: HTTP %{http_code}\n' \
    -X POST http://127.0.0.1:8787/tasks \
    -H \"Authorization: Bearer \$(cat $remoteFile)\" \
    -d '{\"prompt\":\"\"}'"
  echo "  (400 aqui e o esperado: o token passou, o corpo vazio e que foi recusado)"
else
  echo "  o servico agentd-api nao esta ativo; suba com: task serve-enable"
fi
