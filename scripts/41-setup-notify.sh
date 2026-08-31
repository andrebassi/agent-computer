#!/bin/bash
# Liga o destino dos avisos: sem ele o agente pede take-over e ninguem sabe.
#
# # Por que o arquivo fica em /etc/agentd, e nao em /workspace
#
# /workspace e alcancavel pelo usuario do modelo. O destino dos avisos ali seria
# o modelo escolhendo para onde vao os proprios pedidos de socorro -- e apagar a
# linha bastaria para ele trabalhar sem ninguem olhando.
#
# # Por que o topico e secreto
#
# No ntfy.sh qualquer um que saiba o topico LE e PUBLICA nele. O topico e a
# unica credencial que existe, entao ele vive no pass (bassi/agent-computer/ntfy-url)
# e nunca aparece em log, em argumento de comando ou nesta saida.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

NTFY_URL="$(timeout 30s pass show bassi/agent-computer/ntfy-url 2>/dev/null | head -1)"
if [ -z "$NTFY_URL" ]; then
  echo "🛑 bassi/agent-computer/ntfy-url nao esta no cofre"
  exit 1
fi

echo "=== 1. gravando o destino na maquina ==="
# O conteudo vai pelo STDIN, e nao por argumento: argumento e visivel em `ps`
# para qualquer usuario da maquina, inclusive o do modelo.
printf 'AGENT_WEBHOOK=%s\nAGENT_WEBHOOK_FORMAT=ntfy\n' "$NTFY_URL" \
  | root_ssh "mkdir -p /etc/agentd && cat > /etc/agentd/notify.env && chmod 0600 /etc/agentd/notify.env && chown root:root /etc/agentd/notify.env" 2>&1 | sed 's/^/  /'

modo="$(root_ssh "stat -c '%U:%G %a' /etc/agentd/notify.env" 2>&1)"
echo "  arquivo: $modo"
case "$modo" in
  "root:root 600") echo "  ✅ so o root le o destino" ;;
  *) echo "  🛑 dono ou modo inesperado"; exit 1 ;;
esac

echo
echo "=== 2. o usuario do modelo NAO le o destino ==="
# A prova precisa distinguir "nao existe" de "nao posso ver": um `cat` que falha
# por qualquer motivo pareceria contencao funcionando.
leitura="$(root_ssh "sudo -u agent cat /etc/agentd/notify.env 2>&1 || true" 2>&1)"
case "$leitura" in
  *"Permission denied"*) echo "  ✅ recusado por permissao (a mensagem, nao o codigo de saida)" ;;
  *"No such file"*) echo "  🛑 o arquivo sumiu entre os dois passos"; exit 1 ;;
  *) echo "  🛑 o modelo LEU o destino: $leitura"; exit 1 ;;
esac

echo
echo "=== 3. entrega de teste, com um aviso de verdade ==="
# O ntfy responde 200 com um JSON que traz o id da mensagem. Conferir o CODIGO e
# insuficiente: um proxy no caminho tambem devolve 200.
resposta="$(root_ssh "set -a; . /etc/agentd/notify.env; set +a; \
  curl -sS --max-time 15 -H 'Title: agent-computer' -H 'Tags: white_check_mark' \
  -d 'canal ligado: os avisos do agente chegam aqui' \"\$AGENT_WEBHOOK\"" 2>&1)"
case "$resposta" in
  *'"id"'*) echo "  ✅ o ntfy aceitou e devolveu um id de mensagem" ;;
  *) echo "  🛑 resposta inesperada: $resposta"; exit 1 ;;
esac

echo
echo "=== 4. arquivando a fila acumulada antes de ligar a torneira ==="
# A fila tem avisos de suites de teste, de telas que nem existem mais. Drenar
# isso entregaria dezenas de notificacoes de uma vez -- e a primeira coisa que
# alguem faz com um canal que estreia gritando e silencia-lo.
#
# ARQUIVAR, e nao apagar: o historico do que o agente pediu ao longo do dia e
# justamente o que se quer olhar depois. `mv` e reversivel; `rm` nao.
pendentes="$(root_ssh "wc -l < /workspace/agent/events/events.jsonl 2>/dev/null || echo 0" 2>&1 | tr -d ' \r')"
echo "  na fila: ${pendentes:-0}"
if [ "${pendentes:-0}" -gt 0 ]; then
  root_ssh "sudo -u agentd mv /workspace/agent/events/events.jsonl \
    /workspace/agent/events/events-\$(date +%Y%m%d-%H%M).jsonl" 2>&1 | sed 's/^/  /'
  echo "  ✅ arquivada (o arquivo continua no disco, com a data no nome)"
fi

echo
echo "=== 5. a unidade de drenagem enxerga o destino ==="
root_ssh "systemctl start agentd-notify.service; sleep 1; \
  systemctl show agentd-notify -p Result --value; \
  journalctl -u agentd-notify -n 3 --no-pager -o cat" 2>&1 | sed 's/^/  /'

echo
echo "=== 6. o que sobrou na fila ==="
root_ssh "sudo -u agentd agentd -notify-drain 2>&1 | head -1" 2>&1 | sed 's/^/  /'

echo
echo "erros: 0"
