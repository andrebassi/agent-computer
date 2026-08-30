#!/bin/bash
# Gera o token da ASSINATURA do Claude Code, guarda no pass e envia ao droplet.
#
# Assinatura, e nao chave de API: a conta de API do droplet ficou sem saldo em
# 30/08 ("Credit balance is too low") e derrubou a delegacao. O token de
# assinatura usa o plano que o dono ja paga.
#
# O VALOR NUNCA E IMPRESSO. Ele passa por um arquivo temporario com permissao
# 0600, entra no pass, e o arquivo e apagado com shred. Um `echo` aqui deixaria
# a credencial no log, no scrollback e no histórico do terminal.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
load_token

passEntry="bassi/anthropic/claude-code-token"
remoteFile="/workspace/agent/anthropic.env"

# umask antes de qualquer escrita: criar o arquivo e depois corrigir a permissao
# deixa uma janela em que ele existe legivel por todos.
umask 077
tokenFile="$(mktemp "${TMPDIR:-/tmp}/cc-token.XXXXXX")"
cleanup() { command -v shred >/dev/null 2>&1 && shred -u "$tokenFile" 2>/dev/null || rm -f "$tokenFile"; }
trap cleanup EXIT

echo "1/4 lendo o token da assinatura"
# O token entra pelo STDIN, e nao por argumento: argumento aparece em `ps` para
# qualquer processo da maquina, e fica no historico do shell.
#
# Nao e este script que roda `claude setup-token`. Medido em 30/08: ele exige
# terminal de verdade e falha com "tcgetattr/ioctl: Operation not supported on
# socket" quando chamado de um shell sem tty -- nem `script -q` resolve, porque
# o proprio `script` precisa do tty que nao existe. E, sendo um login OAuth, a
# autorizacao e do dono por definicao. Ele roda `claude setup-token` no terminal
# dele e canaliza a saida para ca.
if [ -t 0 ]; then
  echo "🛑 este script le o token do STDIN. Uso:"
  echo "     claude setup-token | $0"
  echo "   ou, com o token ja em maos:"
  echo "     pbpaste | $0"
  exit 1
fi
cat > "$tokenFile"

# O token e extraido por FORMATO, e nao por posicao: o texto ao redor muda entre
# versoes do CLI, a forma do token nao.
token="$(grep -oE 'sk-ant-oat[A-Za-z0-9_-]+' "$tokenFile" | head -1)"
if [ -z "$token" ]; then
  echo "🛑 nao achei um token no formato sk-ant-oat... na entrada"
  exit 1
fi
# 13 caracteres e exatamente `sk-ant-oat01-`: o prefixo de FORMATO, e nenhum
# byte do segredo. Serve para confirmar que veio um token de assinatura e nao
# uma chave de API, que e o engano provavel aqui.
echo "  token lido: ${#token} caracteres, prefixo $(printf '%.13s' "$token")"

echo
echo "2/4 guardando no pass em $passEntry"
printf '%s\n' "$token" | pass insert -m -f "$passEntry" >/dev/null
echo "  gravado"

echo
echo "3/4 enviando ao droplet, substituindo a chave de API sem saldo"
# A chave antiga SAI. Deixar as duas no arquivo faria a de API vencer conforme a
# ordem de leitura, e a delegacao voltaria a falhar por saldo -- com a
# aparencia de que o token nao funcionou.
agent_ssh "umask 077 && cat > $remoteFile" <<ARQUIVO
# Credencial do agente de codigo. Assinatura, nao chave de API.
# Gerado por scripts/20-setup-subscription-token.sh
CLAUDE_CODE_OAUTH_TOKEN=$token
ARQUIVO
agent_ssh "chmod 600 $remoteFile && stat -c 'permissao %a, %s bytes' $remoteFile"

echo
echo "4/4 conferindo pelo EFEITO"
agent_ssh "export CLAUDE_CONFIG_DIR=/workspace/agent/claude-config; \
  set -a; . $remoteFile; set +a; \
  timeout 30s claude auth status 2>&1 | head -6"
