#!/bin/bash
# Teste PONTA A PONTA de tudo o que foi construido, na maquina de verdade.
#
# # O que ele e, e o que ele NAO e
#
# As tres suites existentes cobrem cada uma um recorte:
#
#   08-validate         a maquina: units da tela, portas, firewall, X, Chrome, pixel
#   27-privilege-test   a separacao: o modelo nao alcanca cofre, root nem o binario
#   25-serve-test       a porta HTTP: tarefa, conflito, reconciliacao, fila de avisos
#
# Este aqui NAO as repete. Ele cobre o que fica ENTRE elas -- as costuras que
# cada suite pressupoe e nenhuma exercita:
#
#   - o cofre e mesmo a origem dos segredos do servico, e nao o arquivo antigo
#   - a delegacao roda REBAIXADA, nao como dono do cofre
#   - as habilidades e conectores chegam ao modelo
#   - o estado sobrevive a um restart do servico
#   - os dois caminhos de deploy (Ubuntu e NixOS) atendem o mesmo contrato
#
# # Por que ele nao chama o modelo
#
# Uma tarefa de verdade custa token e demora minutos, e o que ela provaria de
# NOVO -- que o laco funciona -- ja e coberto por 92% de testes em processo. O
# que so a maquina prova e a fiacao: permissao, caminho, usuario, unidade.
#
# Nao aborta na primeira falha: soma os erros, para uma execucao mostrar tudo.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }
skip() { echo "  ⏭️  $1"; }

osName="$(agent_os)"
echo "teste ponta a ponta em $(droplet_ip) — sistema: $osName"
[ "$osName" = "desconhecido" ] && { echo "🛑 nao consegui identificar o sistema"; exit 1; }

echo
echo "=== 1. o binario responde, e SEM erro ==="
# Conferir apenas "saiu alguma coisa" nao serve, e nao e hipotese: a primeira
# versao desta secao aprovava com a saida
#
#   erro: restringindo secrets: chmod /workspace/agent/connectors/secrets: ...
#
# porque a string nao estava vazia. Verificacao que aceita qualquer saida
# aprova justamente o caso que ela existe para pegar.
saida="$(agent_ssh '/usr/local/bin/agentd -catalog list 2>&1' | tr -d '\r')"
if echo "$saida" | grep -qiE '^erro|panic|permission denied'; then
  fail "agentd reclamou: $(echo "$saida" | grep -iE '^erro|panic|permission denied' | head -1 | cut -c1-70)"
elif echo "$saida" | grep -q 'CONECTORES'; then
  ok "agentd executa e lista o catalogo"
else
  fail "saida inesperada: $(echo "$saida" | head -1 | cut -c1-60)"
fi

echo
echo "=== 2. o COFRE e a origem dos segredos do servico ==="
# A prova que importa nao e "o servico subiu", e sim "ele leu do cofre".
# Cair para o arquivo tambem sobe, e a diferenca so aparece no log.
origem="$(agent_ssh 'sudo -n journalctl -u agentd-api.service -n 60 --no-pager 2>/dev/null | grep -o "origem=[a-z]*" | tail -1' | tr -d '\r')"
case "$origem" in
  origem=cofre) ok "o token da porta veio do cofre" ;;
  origem=*)     fail "o token veio de outro lugar: $origem" ;;
  *)            fail "o log nao diz a origem do token" ;;
esac
# E o cofre precisa ABRIR de verdade -- gravar nele funciona mesmo orfao.
if agentd_run '-vault-check -state /workspace/agent' >/dev/null 2>&1; then
  ok "o cofre abre com a identidade desta maquina"
else
  fail "o cofre NAO abre (identidade perdida numa reconstrucao?)"
fi

echo
echo "=== 3. o modelo nao alcanca o cofre, mas o servico alcanca ==="
agent_ssh 'cat /etc/agentd/vault.pass' >/dev/null 2>&1 \
  && fail "o usuario do modelo LE a senha do cofre" \
  || ok "a senha do cofre esta fora do alcance do modelo"

echo
echo "=== 4. a DELEGACAO roda rebaixada ==="
# Foi um descuido real: o rebaixamento entrou na ferramenta de shell e ficou de
# fora da delegacao, e o agente de codigo -- que executa comando arbitrario por
# desenho -- rodava como dono do cofre.
toolUser="$(agent_ssh "systemctl show agentd-api -p Environment --value 2>/dev/null | tr ' ' '\n' | grep AGENTD_TOOL_USER" | tr -d '\r')"
serviceUser="$(agent_ssh 'systemctl show agentd-api -p User --value 2>/dev/null' | tr -d '\r')"
if [ -z "$toolUser" ]; then
  fail "AGENTD_TOOL_USER ausente: as ferramentas rodam como o proprio servico"
elif [ "${toolUser#*=}" = "$serviceUser" ]; then
  fail "rebaixamento NOMINAL: ferramentas e servico sao o mesmo usuario ($serviceUser)"
else
  ok "ferramentas caem de '$serviceUser' para '${toolUser#*=}'"
fi

echo
echo "=== 5. o agente de codigo executa nesta maquina ==="
# Em NixOS isto e onde um binario pre-compilado do npm falha, e a mensagem
# ("Could not start dynamically linked executable") nao aponta para o npm.
codeVersion="$(agent_ssh 'claude --version 2>&1 | head -1' | tr -d '\r')"
if echo "$codeVersion" | grep -qE '^[0-9]+\.[0-9]+'; then
  ok "agente de codigo: $codeVersion"
else
  fail "agente de codigo nao executa: $(echo "$codeVersion" | cut -c1-60)"
fi

echo
echo "=== 6. habilidades e conectores chegam ao modelo ==="
catalogo="$(agentd_run '-catalog list' 2>/dev/null | tr -d '\r')"
conectores="$(echo "$catalogo" | grep -cE '^\s+@' || true)"
habilidades="$(echo "$catalogo" | grep -cE '^\s+/' || true)"
[ "${conectores:-0}" -gt 0 ] && ok "$conectores conector(es) instalado(s)" || fail "nenhum conector"
[ "${habilidades:-0}" -gt 0 ] && ok "$habilidades habilidade(s) instalada(s)" || fail "nenhuma habilidade"

echo
echo "=== 7. o modelo NAO reescreve as proprias regras ==="
for alvo in /workspace/agent/skills /workspace/agent/connectors; do
  agent_ssh "touch $alvo/prova-de-escrita" 2>/dev/null \
    && { fail "o modelo escreve em $alvo"; agent_ssh "rm -f $alvo/prova-de-escrita" >/dev/null 2>&1; } \
    || ok "$alvo somente leitura para o modelo"
done

echo
echo "=== 8. o ESTADO sobrevive ao restart do servico ==="
# Distingue estado duravel de estado em memoria. Uma tarefa criada antes precisa
# continuar consultavel depois -- e o supervisor precisa reconciliar sozinho.
antes="$(agent_ssh 'ls /workspace/agent/tasks/*.json 2>/dev/null | wc -l' | tr -d ' \r')"
agent_ssh 'sudo -n systemctl restart agentd-api' >/dev/null 2>&1
sleep 5
depois="$(agent_ssh 'ls /workspace/agent/tasks/*.json 2>/dev/null | wc -l' | tr -d ' \r')"
saude="$(agent_ssh 'timeout 10s curl -sS --max-time 5 http://127.0.0.1:8787/health' | tr -d '\r')"
[ "${antes:-0}" = "${depois:-x}" ] && ok "as $antes tarefas gravadas sobreviveram" || fail "tarefas antes=$antes depois=$depois"
echo "$saude" | grep -q '"status":"ok"' && ok "a porta voltou depois do restart" || fail "a porta nao voltou: $saude"

echo
echo "=== 9. a tela esta desenhando de verdade ==="
# Nao basta o Chrome estar no ar: uma tela sem fonte desenha caixas, e uma
# captura vazia tambem "funciona".
bytes="$(agent_ssh 'DISPLAY=:1 timeout 20s scrot -o /tmp/e2e.png 2>/dev/null && stat -c %s /tmp/e2e.png' | tr -d ' \r')"
if [ "${bytes:-0}" -gt 20000 ]; then
  ok "captura com $bytes bytes"
else
  fail "captura vazia ou minuscula: ${bytes:-0} bytes"
fi

echo
echo "=== 10. a fila de avisos e ESCRITA pelo servico e LIDA pelo drenador ==="
# Os dois rodam como o mesmo usuario, e ja divergiram: a unidade do drenador
# ficou como `agent` numa separacao de usuarios e a proatividade quebrou em
# silencio -- os avisos eram enfileirados e nunca sairiam.
escritor="$(agent_ssh 'systemctl show agentd-api -p User --value 2>/dev/null' | tr -d '\r')"
leitor="$(agent_ssh 'systemctl show agentd-notify -p User --value 2>/dev/null' | tr -d '\r')"
[ -n "$escritor" ] && [ "$escritor" = "$leitor" ] \
  && ok "escritor e leitor da fila sao o mesmo usuario ($escritor)" \
  || fail "escritor='$escritor' leitor='$leitor' — o drenador nao vai conseguir ler"
agentd_run '-notify-drain -state /workspace/agent' >/dev/null 2>&1 \
  && ok "o drenador roda" || fail "o drenador falhou"

echo
echo "=== 11. o timer de avisos esta armado ==="
agent_ssh 'systemctl is-active agentd-notify.timer' 2>/dev/null | grep -q active \
  && ok "timer ativo" || fail "timer inativo"

echo
echo "=== 12. o CONTRATO e o mesmo nos dois caminhos de deploy ==="
# O que precisa valer em Ubuntu E em NixOS, para trocar de caminho nao ser uma
# aposta. Cada item aqui ja quebrou uma vez ao trocar.
declare -a contrato=(
  "/usr/local/bin/agentd|binario do servico"
  "/etc/agentd/vault.pass|senha do cofre"
  "/workspace/agent/vault|cofre cifrado"
  "/workspace/agent/tasks|estado das tarefas"
)
for item in "${contrato[@]}"; do
  caminho="${item%%|*}"; nome="${item#*|}"
  root_ssh "test -e $caminho" >/dev/null 2>&1 \
    && ok "$nome existe em $caminho" \
    || fail "$nome AUSENTE em $caminho"
done
# E os usuarios, que sao o que da sentido as permissoes acima.
ids="$(root_ssh 'id -u agent; id -u agentd' | tr -d '\r' | tr '\n' ' ')"
read -r uidAgent uidAgentd <<< "$ids"
[ -n "${uidAgent:-}" ] && [ -n "${uidAgentd:-}" ] && [ "$uidAgent" != "$uidAgentd" ] \
  && ok "usuarios distintos: agent=$uidAgent agentd=$uidAgentd" \
  || fail "usuarios ausentes ou iguais: '$ids'"

echo
echo "erros: $errs"
exit $errs
