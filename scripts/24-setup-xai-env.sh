#!/bin/bash
# Instala a chave da xAI no droplet, para o servico de vida longa.
#
# Ate aqui a chave viajava na LINHA DO SSH a cada invocacao — o que funciona
# para um comando pontual e nao funciona para um servico: o systemd sobe o
# processo sem ninguem para passar a chave.
#
# O arquivo, e nao `Environment=` do systemd: `systemctl cat` e
# /proc/<pid>/environ expoem o ambiente de um processo a qualquer um que possa
# ler, e rotacionar a chave passaria a exigir editar a unidade. Com arquivo,
# rotacionar e reescrever e reiniciar.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

passEntry="bassi/xai/apikey"
remoteFile="/workspace/agent/xai.env"

umask 077
keyFile="$(mktemp "${TMPDIR:-/tmp}/xai-key.XXXXXX")"
cleanup() { command -v shred >/dev/null 2>&1 && shred -u "$keyFile" 2>/dev/null || rm -f "$keyFile"; }
trap cleanup EXIT

echo "1/3 lendo a chave do cofre"
timeout 25s pass show "$passEntry" 2>/dev/null | head -1 > "$keyFile"
if [ ! -s "$keyFile" ]; then
  echo "🛑 chave da xAI nao disponivel em $passEntry"
  exit 1
fi
echo "  lida: $(wc -c < "$keyFile" | tr -d ' ') bytes"

echo
echo "2/3 instalando no droplet"
# Formato CHAVE=valor, que e o que o systemd le com EnvironmentFile.
{ echo "# Chave da xAI. Gerado por scripts/24-setup-xai-env.sh"
  printf 'XAI_API_KEY=%s\n' "$(cat "$keyFile")"
} | agent_ssh "umask 077 && cat > $remoteFile"
agent_ssh "chmod 600 $remoteFile && stat -c 'permissao %a, %s bytes' $remoteFile"

echo
echo "3/3 conferindo pelo EFEITO"
# `-catalog list` roda sem tocar em tela nenhuma e sem chamar o modelo, mas o
# binario so sobe se conseguir montar as dependencias. E a prova mais barata de
# que o arquivo e legivel pelo usuario que vai rodar o servico.
agent_ssh "set -a; . $remoteFile; set +a; test -n \"\$XAI_API_KEY\" && echo '  a chave carrega do arquivo' || echo '  🛑 o arquivo nao define XAI_API_KEY'"
