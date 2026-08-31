#!/bin/bash
# Liga o destino dos avisos: sem ele o agente pede take-over e ninguem sabe.
#
# # Por que o arquivo fica em /etc/agentd, e nao em /workspace
#
# /workspace e alcancavel pelo usuario do modelo. O destino dos avisos ali seria
# o modelo escolhendo para onde vao os proprios pedidos de socorro -- e apagar a
# linha bastaria para ele trabalhar sem ninguem olhando.
#
# # O topico e PUBLICO, por escolha do dono
#
# No ntfy.sh qualquer um que saiba o nome do topico LE tudo o que passa por ele e
# PUBLICA nele. O topico em uso e `agent-computer` -- curto e adivinhavel, de
# proposito: e o que deixa o canal utilizavel de qualquer lugar com um
# `curl -d "texto" ntfy.sh/agent-computer`.
#
# A consequencia esta registrada e aceita: os avisos citam a tela, o motivo do
# bloqueio e um trecho da tarefa, e um aviso FALSO pode aparecer ali. Fechar
# depois nao exige trocar de ferramenta -- basta um nome longo e aleatorio, ou
# ntfy com token; o campo AGENT_WEBHOOK aceita os dois sem mudanca de codigo.
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
# AGENT_WEBHOOK pode trazer VARIOS destinos, cada um com prefixo de formato --
# entao o teste sonda cada URL separadamente, em vez de mandar um curl para a
# string inteira (que viraria uma URL invalida com virgula no meio).
destinos="$(root_ssh "set -a; . /etc/agentd/notify.env; set +a; echo \"\$AGENT_WEBHOOK\"" 2>&1 | tr ',' '\n')"
total=0; aceitos=0
while IFS= read -r item; do
  item="$(printf '%s' "$item" | tr -d ' \r')"
  [ -z "$item" ] && continue
  # tira o prefixo de formato, quando houver
  url="${item#*=}"
  case "$item" in ntfy=*|raw=*) : ;; *) url="$item" ;; esac
  total=$((total + 1))
  # `< /dev/null` e OBRIGATORIO aqui: dentro de um laco, o ssh le o stdin ate o
  # fim e engole as linhas restantes -- o laco roda UMA vez e para, sem erro.
  # Medido em 31/08/2026: com dois destinos configurados, o teste imprimiu
  # "destinos: 1 de 1 aceitaram" e o segundo nunca foi sondado.
  #
  # Nao se resolve com `ssh -n` na funcao compartilhada: outros usos entregam
  # conteudo pelo stdin de proposito (`printf ... | root_ssh "cat > arquivo"`).
  code="$(root_ssh "curl -sS -o /dev/null -w '%{http_code}' --max-time 15 \
    -H 'Title: agent-computer' -H 'Tags: white_check_mark' \
    -d 'canal ligado: os avisos do agente chegam aqui' '$url'" </dev/null 2>&1 | tr -dc '0-9')"
  if [ "${code:-0}" -ge 200 ] && [ "${code:-0}" -lt 300 ]; then
    aceitos=$((aceitos + 1))
    echo "  ✅ aceitou (HTTP $code): $(printf '%s' "$url" | cut -c1-48)…"
  else
    echo "  🛑 recusou (HTTP ${code:-sem resposta}): $url"
  fi
done <<< "$destinos"
echo "  destinos: $aceitos de $total aceitaram"
[ "$aceitos" -gt 0 ] || exit 1

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
