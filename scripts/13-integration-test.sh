#!/bin/bash
# Teste integrado: exercita TODAS as cláusulas implementadas de uma vez, na
# máquina de verdade, contra o Grok de verdade.
#
# Os testes unitários provam cada peça isolada; este prova que elas funcionam
# juntas. É a diferença entre "o parser separa marcadores" e "uma tarefa escrita
# com @ e / roda até o fim usando as ferramentas certas".
#
# Não aborta na primeira falha: soma os erros e devolve o total, para uma
# execução mostrar tudo que está quebrado em vez de um por vez.
source "$(dirname "$0")/lib.sh"
source "$(dirname "$0")/suite-lock.sh"
suite_lock "$(basename "$0")"
set -uo pipefail
load_token

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

ip="$(droplet_ip)"
[ -z "$ip" ] && { echo "🛑 droplet nao existe — rodar task up"; exit 1; }
echo "teste integrado em $ip"


# agentd roda com a chave no AMBIENTE, nunca em linha de comando: `ps` exporia
# o valor a qualquer processo da maquina.
run_agent() {
  # Roda como `agentd`, o dono do estado -- e nao como `agent`.
  #
  # Nao e preferencia: o diretorio de travas e 2750 agentd:agent, entao `agent`
  # LE mas nao CRIA arquivo ali. O sintoma era "abrindo arquivo de trava:
  # permission denied" depois de a tarefa ja ter sido criada e a habilidade
  # aplicada -- o trabalho comecava e morria na hora de tomar a tela.
  #
  # Afrouxar o diretorio para 2770 resolveria e seria o remendo errado: o estado
  # tem UM dono, e o CLI e ferramenta de OPERADOR. A autoridade dele e a chave
  # de root, nao uma permissao de grupo.
  #
  # Efeito colateral bem-vindo: a chave do modelo sai da linha de comando (onde
  # `ps` a expunha) e passa a vir do cofre.
  agentd_run "$*"
}

echo
echo "=== 0. preparando o terreno ==="
# O estado é durável de propósito, então uma tarefa bloqueada de uma execução
# anterior CONTINUA travando a tela depois de um rebuild. Sem liberar antes,
# tudo daqui para baixo falha em cascata por um motivo que nada tem a ver com o
# que se quer testar. Foi o que aconteceu na primeira execução deste script.
for tela in 1 2; do
  activeTask="$(agent_ssh "python3 -c \"
import json,glob
for f in glob.glob('/workspace/agent/tasks/*.json'):
    t=json.load(open(f))
    if t['Screen']==$tela and t['State'] in ('pending','running','blocked'):
        print(t['ID']); break
\" 2>/dev/null")"
  if [ -n "$activeTask" ]; then
    echo "  tela $tela ocupada por '$activeTask' — abandonando"
    agent_ssh "/usr/local/bin/agentd -abandon -task '$activeTask'" >/dev/null 2>&1 \
      && ok "tela $tela liberada" || fail "nao consegui liberar a tela $tela"
  else
    ok "tela $tela livre"
  fi
done

echo
echo "=== 1. o estado duravel sobreviveu ao rebuild ==="
if agent_ssh 'mountpoint -q /workspace'; then
  ok "/workspace montado do volume"
else
  fail "/workspace NAO e o volume — o estado se perderia no proximo update"
fi
profileSize="$(agent_ssh 'du -sm /workspace/browser 2>/dev/null | cut -f1')"
if [ -n "$profileSize" ] && [ "$profileSize" -gt 100 ] 2>/dev/null; then
  ok "profileSize do navegador preservado (${profileSize} MB)"
else
  fail "profileSize do navegador ausente ou vazio (${profileSize:-nada})"
fi
oldTasks="$(agent_ssh 'ls /workspace/agent/tasks/*.json 2>/dev/null | wc -l | tr -d " "')"
if [ "${oldTasks:-0}" -gt 0 ]; then
  ok "tarefas de antes do rebuild continuam la (${oldTasks})"
else
  fail "as tarefas anteriores se perderam"
fi

echo
echo "=== 2. as telas subiram sozinhas ==="
for unit in xvfb openbox x11vnc novnc chrome; do
  st="$(agent_ssh "systemctl is-active ${unit}@1.service 2>/dev/null")"
  [ "$st" = "active" ] && ok "${unit}@1" || fail "${unit}@1: ${st:-sem resposta}"
done

echo
echo "=== 3. o agente executa uma tarefa comum ==="
runMark="integrado-$(date +%s)"
out="$(run_agent -screen 1 -task "$runMark-comum" \
  -prompt "'Escreva o texto $runMark no arquivo /workspace/projects/$runMark.txt e confirme que ele existe.'" 2>&1)"
if echo "$out" | grep -q "concluída"; then
  ok "tarefa comum concluida"
else
  fail "tarefa comum nao concluiu: $(echo "$out" | tail -1)"
fi
if [ "$(agent_ssh "cat /workspace/projects/$runMark.txt 2>/dev/null")" = "$runMark" ]; then
  ok "o agente gravou o arquivo com o conteudo certo"
else
  fail "o arquivo nao foi gravado como esperado"
fi

echo
echo "=== 4. conector anexado com @ vira ferramenta ==="
agent_ssh "mkdir -p /workspace/agent/connectors/installed"
# O manifesto YAML de exemplo, copiado para o catalogo.
if agent_ssh "test -f /workspace/agent/connectors/installed/gitlab.yaml"; then
  ok "manifesto do conector instalado"
else
  fail "manifesto do conector ausente"
fi
out="$(run_agent -screen 1 -task "$runMark-conector" \
  -prompt "'@gitlab Diga em uma linha os nomes das ferramentas de conector que voce tem. Nao chame nenhuma.'" 2>&1)"
if echo "$out" | grep -q "conectores anexados: gitlab"; then
  ok "conector reconhecido e anexado"
else
  fail "conector nao foi anexado: $(echo "$out" | head -2)"
fi
modelAnswer="$(agent_ssh "python3 -c \"
import json
d=json.load(open('/workspace/agent/conversations/$runMark-conector.json'))
print(' '.join(m.get('Content') or '' for m in d['messages'] if m['Role']=='assistant'))
\" 2>/dev/null")"
if echo "$modelAnswer" | grep -q "gitlab.list_issues"; then
  ok "o modelo ENXERGA as ferramentas do conector"
else
  fail "o modelo nao citou as ferramentas: ${modelAnswer:0:120}"
fi

echo
echo "=== 5. habilidade referenciada com / entra no prompt ==="
agent_ssh "mkdir -p /workspace/agent/skills && printf 'Responda sempre comecando com a palavra CONFIRMADO.\n' > /workspace/agent/skills/estilo.md"
out="$(run_agent -screen 1 -task "$runMark-skill" \
  -prompt "'/estilo Diga qual e o diretorio durave desta maquina. Nao chame ferramenta.'" 2>&1)"
if echo "$out" | grep -q "habilidades aplicadas: estilo"; then
  ok "habilidade reconhecida e aplicada"
else
  fail "habilidade nao aplicada: $(echo "$out" | head -2)"
fi
sentPrompt="$(agent_ssh "python3 -c \"
import json
d=json.load(open('/workspace/agent/conversations/$runMark-skill.json'))
print(' '.join(m.get('Content') or '' for m in d['messages'] if m['Role']=='user'))
\" 2>/dev/null")"
if echo "$sentPrompt" | grep -q "habilidade salva: estilo"; then
  ok "o bloco da habilidade chegou ao modelo, delimitado"
else
  fail "o bloco nao foi injetado"
fi
if echo "$sentPrompt" | grep -q "/estilo"; then
  fail "o marcador /estilo devia ter saido do texto"
else
  ok "marcador removido do texto enviado ao modelo"
fi

echo
echo "=== 6. caminho de arquivo NAO vira habilidade ==="
out="$(run_agent -screen 1 -task "$runMark-caminho" \
  -prompt "'Liste o conteudo de /workspace/projects e responda em uma linha.'" 2>&1)"
if echo "$out" | grep -q "habilidades aplicadas"; then
  fail "um caminho de arquivo foi tratado como habilidade"
else
  ok "caminho preservado, nenhuma habilidade falsa anexada"
fi

echo
echo "=== 7. take-over: o agente PARA diante de senha ==="
out="$(run_agent -screen 1 -task "$runMark-takeover" \
  -prompt "'Entre no painel https://painel.exemplo.com com o usuario admin. A pagina pede usuario e senha.'" 2>&1)"
if echo "$out" | grep -q "PRECISA DE VOCÊ"; then
  ok "o agente parou e pediu take-over"
else
  fail "o agente NAO pediu ajuda: $(echo "$out" | tail -1)"
fi
taskState="$(agent_ssh "python3 -c \"
import json
print(json.load(open('/workspace/agent/tasks/$runMark-takeover.json'))['State'])
\" 2>/dev/null")"
[ "$taskState" = "blocked" ] && ok "tarefa em blocked" || fail "taskState inesperado: ${taskState:-nada}"

echo
echo "=== 8. a trava de uma tarefa por tela vale ==="
out="$(run_agent -screen 1 -prompt "'qualquer outra coisa'" 2>&1)"
# A mensagem real e "a tela já tem uma tarefa ativa" (domain/task.go).
#
# Aqui houve DOIS defeitos empilhados, e o segundo escondeu o primeiro:
#   1. procurar sem acento reprovava uma trava que funcionava;
#   2. o sweep de renomeacao para o ingles traduziu "ativa" -> "activeTask"
#      DENTRO desta string e do comentario, e ai passou a nao casar com nada.
#
# O segundo e exatamente a armadilha que a regra de idioma descreve: literal de
# string nunca se renomeia, so identificador. Medido em 30/08/2026 -- o teste
# reprovava "a trava nao impediu a segunda tarefa" com a trava intacta, provada
# pelo 409 do `25-serve-integration-test`.
if echo "$out" | grep -q "tem uma tarefa ativa"; then
  ok "segunda tarefa recusada enquanto a tela esta ocupada"
else
  fail "a trava nao impediu a segunda tarefa"
fi

echo
echo "=== 9. o status aparece na tela ==="
status="$(agent_ssh 'cat /workspace/agent/status/screen-1.status 2>/dev/null')"
if echo "$status" | grep -q "PRECISA DE VOCÊ"; then
  ok "a tela mostra o pedido de ajuda"
else
  fail "status inesperado na tela: ${status:-vazio}"
fi

echo
echo "=== 10. retomada devolve o controle ao agente ==="
out="$(run_agent -resume -task "$runMark-takeover" -note "'a senha foi digitada por uma pessoa'" 2>&1)"
taskState="$(agent_ssh "python3 -c \"
import json
print(json.load(open('/workspace/agent/tasks/$runMark-takeover.json'))['State'])
\" 2>/dev/null")"
if [ "$taskState" = "done" ] || [ "$taskState" = "blocked" ]; then
  ok "retomada processada (estado: $taskState)"
else
  fail "retomada nao funcionou (estado: ${taskState:-nada})"
fi

echo
echo "=== 11. segunda tela, compartilhando o mesmo workspace ==="
agent_ssh 'screen-add 2' >/dev/null 2>&1
sleep 3
if [ "$(agent_ssh 'systemctl is-active chrome@2.service 2>/dev/null')" = "active" ]; then
  ok "tela 2 no ar"
else
  fail "tela 2 nao subiu"
fi
out="$(run_agent -screen 2 -task "$runMark-tela2" \
  -prompt "'Leia /workspace/projects/$runMark.txt e diga o conteudo em uma linha.'" 2>&1)"
if echo "$out" | grep -q "concluída"; then
  ok "a tela 2 trabalha no mesmo /workspace da tela 1"
else
  fail "a tarefa na tela 2 nao concluiu"
fi

echo
echo "=== 12. recursos com duas telas ==="
agent_ssh 'free -m | awk "/^Mem:/ {printf \"  RAM: %s MB de %s (%.0f%%)\n\", \$3, \$2, \$3/\$2*100}"'
agent_ssh 'df -h /workspace | tail -1 | awk "{printf \"  volume: %s de %s (%s)\n\", \$3, \$2, \$5}"'

echo
echo "erros: $errs"
exit $errs
