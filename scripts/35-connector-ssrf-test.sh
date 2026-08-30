#!/bin/bash
# Prova, na maquina, que conector nao alcanca a rede interna -- nem por NOME.
#
# # O que estava aberto
#
# `validateBaseURL` recusa `http://169.254.169.254` (o metadata da nuvem) e as
# faixas internas, mas so quando o host e um IP LITERAL. Um NOME que resolve
# para la passava: o cadastro ve um nome, e o nome nao e um IP.
#
# Tres caminhos escapavam de qualquer checagem feita antes da conexao:
#
#   1. rebinding -- o DNS responde um IP publico no cadastro e o interno na hora
#      da chamada;
#   2. redirect -- um 302 leva o cliente para um destino que ninguem validou;
#   3. registros multiplos -- o resolvedor devolve o interno na segunda vez.
#
# A correcao vive no DISCADOR (`agent/internal/adapters/driven/connectors/
# dialer.go`), que ve o IP final no instante de abrir o socket. Este script
# confere o efeito na maquina, que e onde o metadata de fato existe -- no Mac
# nao ha 169.254.169.254 para alcancar, e um teste que passa aqui e la por
# motivos diferentes nao prova a mesma coisa.
#
# # As duas direcoes
#
# Recusar o interno prova metade. A outra metade e o conector legitimo
# continuar funcionando: um discador que recusasse tudo passaria na primeira
# checagem e quebraria todo conector em producao.
source "$(dirname "$0")/lib.sh"
source "$(dirname "$0")/suite-lock.sh"
suite_lock "$(basename "$0")"
set -uo pipefail
load_token

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

echo "=== 1. o metadata da nuvem EXISTE nesta maquina ==="
# Sem esta checagem, o teste seguinte passaria numa maquina onde o metadata
# simplesmente nao responde -- aprovando o bloqueio sem nunca o exercitar.
metadata="$(agent_ssh 'timeout 10s curl -sS --max-time 5 http://169.254.169.254/metadata/v1/id 2>&1 | head -c 40')"
if [ -n "$metadata" ]; then
  ok "o metadata responde ao shell: ${metadata:0:24}..."
else
  fail "o metadata NAO responde -- este teste nao prova nada nesta maquina"
fi

echo
echo "=== 2. o nome que resolve para o metadata e RECUSADO ==="
# `metadata.google.internal` nao resolve fora do GCP. O nome usado aqui e
# `localhost`, que resolve para loopback em qualquer maquina -- e loopback e
# onde moram a porta de tarefas (8787) e o Chrome com depuracao remota (9222),
# ambos sem autenticacao de rede. Alcancar qualquer um deles a partir de um
# conector e o mesmo tipo de defeito que alcancar o metadata.
saida="$(agentd_run "-connector-probe http://localhost:8787/health" 2>&1)"
if printf '%s' "$saida" | grep -qiE 'rede interna|resolve para|própria máquina'; then
  ok "nome que resolve para dentro foi recusado"
else
  fail "o nome NAO foi recusado"
  printf '%s\n' "$saida" | tail -4 | sed 's/^/      /'
fi

echo
echo "=== 3. o IP literal interno continua recusado ==="
saida="$(agentd_run "-connector-probe http://169.254.169.254/metadata/v1/id" 2>&1)"
if printf '%s' "$saida" | grep -qiE 'rede interna|resolve para|própria máquina'; then
  ok "IP do metadata recusado"
else
  fail "o IP do metadata NAO foi recusado"
  printf '%s\n' "$saida" | tail -4 | sed 's/^/      /'
fi

echo
echo "=== 4. destino EXTERNO continua funcionando ==="
# A outra direcao da prova. Sem ela, um discador quebrado que recusa tudo
# passaria nas secoes 2 e 3 e derrubaria todo conector.
saida="$(agentd_run "-connector-probe https://api.frankfurter.app/latest?from=USD&to=BRL" 2>&1)"
if printf '%s' "$saida" | grep -q '"rates"'; then
  ok "conector legitimo alcancou a API externa"
else
  fail "o destino externo NAO foi alcancado -- o bloqueio esta recusando demais"
  printf '%s\n' "$saida" | tail -4 | sed 's/^/      /'
fi

echo
echo "erros: $errs"
exit $errs
