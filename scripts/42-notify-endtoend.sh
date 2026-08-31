#!/bin/bash
# Prova que um aviso NASCIDO de uma tarefa real chega ao destino.
#
# O script 41 provou o CANAL (o ntfy aceita, o modelo nao le o arquivo). Isto
# aqui e diferente e e o que importa: uma tarefa de verdade bloqueia, o evento
# entra na fila, o drenador o entrega, e a fila fica vazia depois.
#
# A distincao existe porque um canal que funciona com `curl` e um agente que
# avisa sao coisas separadas -- entre os dois ha o spool, a unidade, o usuario
# do servico e o formato. Cada um deles ja quebrou em silencio neste projeto.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

fails=0

# queue_size devolve SO um numero, nunca a mensagem de erro junto.
#
# `wc -l < arquivo-inexistente` escreve no stderr do shell REMOTO, e o `2>&1`
# desta ponta o captura junto do "0" -- a comparacao numerica seguinte recebia
# "0\nbash: ... No such file" e o teste reprovava um produto intacto.
queue_size() {
  root_ssh "cat /workspace/agent/events/events.jsonl 2>/dev/null | wc -l" 2>/dev/null \
    | tr -dc '0-9' | head -c 8
}

step() { echo; echo "=== $1 ==="; }
ok()   { echo "  ✅ $1"; }
bad()  { echo "  🛑 $1"; fails=$((fails + 1)); }

# O TIMER e desligado durante o teste, e religado ao sair.
#
# Ele roda a cada 5 min, e drenou a fila no intervalo entre a tarefa bloquear e
# o teste medir: o passo 3 leu 0 e acusou "o bloqueio NAO gerou aviso" enquanto
# o journal dizia "avisos entregues: 1". Verificacao que reprova por corrida
# ensina a ignorar o vermelho -- e vermelho ignorado e o unico defeito que os
# outros testes nao pegam.
#
# `trap` e nao um `systemctl start` no fim: o fim nao roda quando algo aborta, e
# a maquina ficaria sem entrega automatica sem ninguem perceber.
root_ssh "systemctl stop agentd-notify.timer" >/dev/null 2>&1
trap 'root_ssh "systemctl start agentd-notify.timer" >/dev/null 2>&1' EXIT

# A TELA e liberada no COMECO, nao no fim.
#
# Este teste bloqueia uma tarefa de proposito, entao ele deixa a tela ocupada
# atras de si -- e a rodada seguinte tem o pedido recusado por "tela ocupada",
# recebendo de volta a mensagem da tarefa VELHA. O passo 2 casava esse texto e
# aprovava sem ter criado nada; so o passo 3 denunciava, apontando o lugar errado.
#
# Limpar no fim nao resolve: o fim nao roda quando algo aborta no meio.
SCREEN=1
# `-state` e o DIRETORIO de estado, nao "mostre o estado" -- o comando que eu
# usava aqui falhava com "flag needs an argument" e a limpeza nunca acontecia,
# em silencio. A verdade esta no disco, e os campos do JSON sao capitalizados.
pendente="$(root_ssh "grep -l '\"State\": \"blocked\"' /workspace/agent/tasks/*.json 2>/dev/null \
  | xargs -r grep -l '\"Screen\": $SCREEN,' 2>/dev/null | head -1 \
  | xargs -r basename -s .json" 2>/dev/null | tr -d ' \r')"
if [ -n "$pendente" ]; then
  echo "sobra da rodada anterior: $pendente — abandonando"
  root_ssh "sudo -u agentd /usr/local/bin/agentd -abandon -task $pendente" >/dev/null 2>&1
fi

step "1. fila antes (o ponto de partida precisa ser conhecido)"
before="$(queue_size)"
echo "  pendentes: ${before:-0}"

step "2. uma tarefa que BLOQUEIA de verdade"
# Teto de custo em meio centavo: qualquer tarefa estoura no primeiro turno. E o
# caminho real do detector, com um numero menor -- nao uma simulacao.
saida="$(root_ssh "sudo -u agentd env AGENTD_MAX_COST_USD=0.0005 \
  /usr/local/bin/agentd -screen $SCREEN -prompt 'diga apenas: teste de aviso' 2>&1" 2>&1)"
# O id da tarefa que ESTE teste criou. Sem ele, uma mensagem de tarefa antiga
# passa por criacao bem-sucedida -- foi exatamente o que aconteceu.
criada="$(printf '%s' "$saida" | grep -oE 'task-[0-9]+' | head -1)"
# A saida INTEIRA e julgada; so a exibicao e cortada. Cortar antes escondeu a
# linha do bloqueio e o teste reprovou um produto que funcionava.
echo "$saida" | tail -4 | sed 's/^/  /'
case "$saida" in
  *"tela ocupada"*|*"ErrScreenBusy"*)
    bad "a tela $SCREEN continua ocupada; a limpeza inicial nao pegou" ;;
  *"limite de segurança"*|*"PRECISA DE VOCÊ"*)
    if [ -n "$criada" ]; then
      ok "a tarefa $criada parou no teto, como esperado"
    else
      bad "bloqueou, mas sem id: pode ser mensagem de tarefa anterior"
    fi ;;
  *) bad "a tarefa nao bloqueou; sem bloqueio nao ha aviso para entregar" ;;
esac
# Libera a tela ASSIM QUE o aviso ja foi gerado: o proposito do bloqueio se
# cumpriu no passo 3, e deixar a tarefa parada so atrapalha a proxima rodada.
[ -n "$criada" ] && trap "root_ssh \"sudo -u agentd /usr/local/bin/agentd -abandon -task $criada\" >/dev/null 2>&1; root_ssh \"systemctl start agentd-notify.timer\" >/dev/null 2>&1" EXIT

step "3. o evento entrou na fila"
after="$(queue_size)"
echo "  pendentes: ${after:-0}"
if [ "${after:-0}" -gt "${before:-0}" ]; then
  ok "o bloqueio virou aviso enfileirado"
else
  bad "o bloqueio NAO gerou aviso"
fi

step "4. o drenador entrega e ESVAZIA a fila"
# Rodar a unidade, e nao o binario na mao: o binario chamado por mim nao le o
# /etc/agentd/notify.env (root:root 0600), e o teste passaria por engano.
root_ssh "systemctl start agentd-notify.service" >/dev/null 2>&1
resultado="$(root_ssh "systemctl show agentd-notify -p Result --value" 2>&1 | tr -d ' \r')"
echo "  resultado da unidade: ${resultado:-desconhecido}"
[ "$resultado" = "success" ] && ok "a unidade terminou limpa" || bad "a unidade falhou"

final="$(queue_size)"
echo "  pendentes depois: ${final:-0}"
if [ "${final:-0}" -eq 0 ]; then
  ok "a fila esvaziou: a entrega foi CONFIRMADA pelo destino"
else
  bad "sobrou aviso na fila -- o destino recusou ou esta fora do ar"
fi

step "5. o timer volta a rodar sozinho"
# Conferir DEPOIS de religar: um teste que deixa a entrega automatica desligada
# e pior que teste nenhum -- o proximo pedido de take-over ficaria parado.
root_ssh "systemctl start agentd-notify.timer" >/dev/null 2>&1
timer="$(root_ssh "systemctl is-active agentd-notify.timer" 2>&1 | tr -d ' \r')"
[ "$timer" = "active" ] && ok "o timer de 5 min esta de pe" || bad "o timer ficou parado: $timer"

step "6. o que o journal registrou"
root_ssh "journalctl -u agentd-notify -n 4 --no-pager -o cat" 2>&1 | sed 's/^/  /'

echo
echo "erros: $fails"
[ "$fails" -eq 0 ]
