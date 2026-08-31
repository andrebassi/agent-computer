#!/bin/bash
# Prova, NA MAQUINA, que o segredo de conector some do historico se voltar.
#
# # O caminho de vazamento que este teste reproduz
#
# O modelo NAO alcanca o segredo: o arquivo e 0600 do `agentd`, e o `env` do
# usuario do modelo nao o tem. Isso ja e verificado pelo teste de privilegio.
#
# O caminho que sobra e o unico que importa aqui: a API REMOTA devolve o
# cabecalho de volta. Acontece de verdade -- endpoint de eco, mensagem de erro
# que cita o header recebido, resposta de depuracao. Nesse caso o segredo entra
# na saida da ferramenta, dali no historico, e o historico:
#
#   * vai para o modelo em TODA iteracao seguinte;
#   * e gravado no volume, que nao expira (decisao aceita, ver SECURITY.md);
#   * entra em cada foto do volume.
#
# # Por que este teste precisa existir
#
# O mecanismo de redacao existia INTEIRO e nunca protegeu nada: `TrackSecret` so
# era chamado por teste, entao `Redact` percorria uma lista vazia em toda
# mensagem. Codigo escrito, testado em isolamento, e nunca ligado -- o mesmo
# padrao de `ToolResult.Failed` e de `RecordProgress`.
#
# Testar em processo prova a logica. So aqui se prova que o caminho de producao
# arma a lista.
source "$(dirname "$0")/lib.sh"
source "$(dirname "$0")/suite-lock.sh"
suite_lock "$(basename "$0")"
set -uo pipefail
load_token

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

STATE=/workspace/agent
# Valor sem cara de credencial real: o gate de segredo do repositorio barra
# literal com formato de token, e ele esta certo em barrar.
CANARIO="VALOR-DE-ECO-PARA-TESTE-DE-REDACAO"

echo "=== 1. instalando um conector que faz a API DEVOLVER o cabecalho ==="
# postman-echo.com/get responde com os cabecalhos que recebeu. E o cenario real
# de vazamento, reproduzido de forma controlada.
root_ssh "cat > /tmp/eco.yaml" <<'MANIFESTO'
name: ecoteste
description: Conector de teste que faz a API devolver o cabecalho de autenticacao.
base_url: https://postman-echo.com
auth:
  type: header
  header_name: X-Segredo-De-Teste
  secret_ref: ecoteste-token
operations:
  - name: eco
    description: Devolve os cabecalhos recebidos.
    method: GET
    path: /get
    schema:
      type: object
      properties: {}
MANIFESTO

root_ssh "setpriv --reuid=agentd --regid=agentd --init-groups -- /usr/local/bin/agentd -catalog install /tmp/eco.yaml" >/dev/null 2>&1
# A credencial e escrita DIRETO no arquivo, e nao por `-catalog secret`.
#
# O comando recusa entrada que nao venha de terminal, de proposito: ler segredo
# de um cano leria de um script ou de um log, que e exatamente por onde segredo
# vaza. A recusa esta certa, e foi ela que fez a primeira versao deste teste
# passar sem exercitar nada -- o conector ficou sem credencial, a ferramenta
# falhava antes de chegar a API, e a secao 3 dizia so "o modelo nao chamou".
#
# Num teste, escrever o arquivo e legitimo e explicito: e o mesmo que o comando
# faria no fim, com o mesmo dono e o mesmo modo.
root_ssh "install -o agentd -g agentd -m 600 /dev/null $STATE/connectors/secrets/ecoteste-token &&
  printf '%s' '$CANARIO' > /tmp/eco-secret &&
  install -o agentd -g agentd -m 600 /tmp/eco-secret $STATE/connectors/secrets/ecoteste-token &&
  rm -f /tmp/eco-secret" >/dev/null 2>&1

written="$(root_ssh "test -s $STATE/connectors/secrets/ecoteste-token && echo existe" 2>/dev/null)"
if printf '%s' "$written" | grep -q existe; then
  ok "credencial de teste gravada"
else
  fail "a credencial nao foi gravada; a secao 3 nao vai exercitar nada"
fi

if agentd_run "-catalog list" 2>&1 | grep -q "ecoteste"; then
  ok "conector de eco instalado"
else
  fail "nao consegui instalar o conector de eco"
fi

echo
echo "=== 2. a API DE FATO devolve o segredo (senao o teste nao prova nada) ==="
# Sem esta secao, um endpoint que parasse de ecoar tornaria o teste verde sem
# exercitar a redacao -- e ninguem notaria.
direct="$(root_ssh "curl -sS --max-time 20 -H 'X-Segredo-De-Teste: $CANARIO' https://postman-echo.com/get" 2>&1)"
if printf '%s' "$direct" | grep -q "$CANARIO"; then
  ok "a API devolve o cabecalho recebido"
else
  fail "a API NAO ecoa mais o cabecalho; este teste perdeu o alvo"
  printf '%s' "$direct" | head -c 200 | sed 's/^/      /'
fi

echo
echo "=== 3. o segredo NAO sobrevive no historico da tarefa ==="
output="$(agentd_run "-screen 3 -prompt \"@ecoteste chame a operacao eco e me diga exatamente o que voltou\"" 2>&1)"
task="$(printf '%s' "$output" | sed -n 's/.*\(task-[0-9]\{6,\}\).*/\1/p' | head -1)"

if [ -z "$task" ]; then
  fail "a tarefa nao foi criada"
  printf '%s' "$output" | tail -3 | sed 's/^/      /'
else
  verdict="$(agent_ssh "python3 -c \"
import json
c = json.load(open('$STATE/conversations/$task.json'))
alvo = '$CANARIO'
vazou = any(alvo in (m.get('Content') or '') for m in c.get('messages', []))
# A prova de que a ferramenta rodou e a CHAMADA registrada, nao o texto
# 'postman-echo' no conteudo -- esse casa com a descricao da ferramenta no
# prompt de sistema, e dava verde sem nada ter sido chamado.
tocou = any(
    (call.get('Name') or '').startswith('ecoteste')
    for m in c.get('messages', [])
    for call in (m.get('ToolCalls') or [])
)
# 'X-Segredo-De-Teste' aparece na RESPOSTA da API, ecoada de volta. Sem isso o
# segredo nunca chegou ao historico e a redacao nao foi exercitada -- que e
# diferente de ter funcionado.
ecoou = any('X-Segredo-De-Teste' in (m.get('Content') or '') or 'x-segredo-de-teste' in (m.get('Content') or '')
            for m in c.get('messages', []) if m.get('Role') == 'tool')
print('vazou' if vazou else 'limpo', '|', 'chamou' if tocou else 'nao-chamou', '|', 'ecoou' if ecoou else 'nao-ecoou')
\" 2>/dev/null" | tr -d '\r')"

  case "$verdict" in
    "limpo | chamou | ecoou")
      # O unico veredito que prova alguma coisa: a API devolveu o cabecalho, o
      # texto entrou no historico, e o segredo nao esta la.
      ok "a API ecoou o cabecalho e o segredo NAO sobreviveu no historico"
      ;;
    "vazou |"*)
      fail "O SEGREDO ESTA NO HISTORICO -- a redacao nao esta armada"
      ;;
    "limpo | chamou | nao-ecoou")
      # Verde enganoso: a ferramenta rodou, mas o segredo nunca chegou ao
      # historico. Nao ha o que redigir, e dizer que passou seria mentir.
      fail "a resposta nao trouxe o cabecalho; a redacao NAO foi exercitada"
      ;;
    "limpo | nao-chamou"*)
      fail "o modelo nao chamou o conector; a redacao nao foi exercitada"
      ;;
    *)
      fail "veredito inesperado: $verdict"
      ;;
  esac
fi

echo
echo "=== 4. o modelo continua sem alcancar o arquivo do segredo ==="
# Defesa em profundidade: a redacao existe para o caso de o segredo CHEGAR ao
# historico. A primeira barreira e o modelo nao conseguir le-lo.
# A saida vai para uma VARIAVEL antes de ser testada, e nao direto para um pipe.
#
# Com `set -o pipefail`, `comando_que_falha | grep -q` devolve o rc do PRIMEIRO
# elo. O `cat` remoto falha legitimamente (a permissao funcionou), o `grep` casa,
# e o `if` recebe 1 mesmo assim -- reprovando exatamente o comportamento que se
# queria confirmar.
#
# Medido em 31/08/2026, neste script: a secao dizia "o usuario do modelo LE o
# arquivo do segredo" com o diretorio em drwx------ e o cat devolvendo
# "Permission denied". Alarme falso na direcao mais perigosa: teria mandado
# afrouxar uma permissao correta.
denial="$(agent_ssh "cat $STATE/connectors/secrets/ecoteste-token 2>&1")"
if printf '%s' "$denial" | grep -qiE "permission denied|permissão negada|no such file"; then
  ok "o usuario do modelo nao le o arquivo do segredo"
else
  fail "o usuario do modelo LE o arquivo do segredo: ${denial:0:60}"
fi

echo
echo "=== 5. limpando o conector de teste ==="
root_ssh "setpriv --reuid=agentd --regid=agentd --init-groups -- /usr/local/bin/agentd -catalog remove ecoteste" >/dev/null 2>&1
root_ssh "rm -f /tmp/eco.yaml" >/dev/null 2>&1
if agentd_run "-catalog list" 2>&1 | grep -q "ecoteste"; then
  fail "o conector de teste ficou instalado"
else
  ok "conector de teste removido"
fi

echo
echo "erros: $errs"
exit $errs
