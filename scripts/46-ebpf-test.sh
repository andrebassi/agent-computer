#!/bin/bash
# Prova que o coletor eBPF VÊ o que diz ver — e que reprova quando não vê.
#
# É a camada de verificação do pacote `collector`, que o gate de cobertura
# exclui por não ter como rodar no Mac. Excluir do gate e dizer onde a prova
# está é diferente de abrir mão dela; este script é o "onde".
#
# TRÊS PROPRIEDADES DE DESENHO, cada uma vinda de um erro já pago neste
# repositório:
#
#  1. GATILHO DETERMINÍSTICO, nunca pedido ao modelo. Um canário copiado de
#     `/bin/true` com nome aleatório roda quando mandado, sempre. Teste que
#     depende do modelo cooperar é intermitente por construção — está medido no
#     README: pedindo repetição explícita, o modelo repetiu duas vezes e parou.
#
#  2. ASSERÇÃO SOBRE O NOME EXATO, não sobre "chegou alguma coisa". Um coletor
#     que emitisse lixo passaria num teste de "veio algum evento". O nome
#     aleatório do canário não pode aparecer por acaso.
#
#  3. PROVA DE FALHA NOS DOIS SENTIDOS. Com o coletor no ar tem que achar; sem
#     ele, tem que NÃO achar. Só o segundo prova que o teste enxerga alguma
#     coisa — uma verificação que passa nos dois estados não verifica nada.
source "$(dirname "$0")/lib.sh"
source "$(dirname "$0")/suite-lock.sh"
set -uo pipefail
load_token
suite_lock "$(basename "$0")"

failures=0

# fail registra uma reprovação sem abortar: um script que para na primeira
# esconde as outras, e o operador conserta uma por rodada.
fail() {
  echo "  🛑 $1"
  failures=$((failures + 1))
}

echo "=== 1/6 o coletor está instalado e é de root? ==="
listing="$(root_ssh 'ls -l /usr/local/bin/agent-probe 2>/dev/null' 2>/dev/null | tr -d '\r')"
if [ -z "$listing" ]; then
  fail "agent-probe não está instalado — rode ./scripts/45-deploy-probe.sh"
  echo; echo "erros: $failures"; exit 1
fi
echo "  $listing"
# A contenção se verifica pelo COMPORTAMENTO, não pelo modo do arquivo: o que
# importa é o que o usuário do modelo consegue fazer.
if agent_ssh 'test -w /usr/local/bin/agent-probe' 2>/dev/null; then
  fail "o usuário 'agent' escreve no binário do coletor"
else
  echo "  ✅ o usuário 'agent' não escreve nele"
fi

echo
echo "=== 2/6 com o coletor NO AR, o canário aparece? ==="
# O canário é copiado, e não é um binário existente: um nome aleatório não pode
# aparecer no registro por acaso, e é isso que torna a asserção conclusiva.
withProbe="$(root_ssh '
canary="/tmp/probe-canary-$RANDOM-$$"
cp "$(readlink -f "$(command -v sleep)")" "$canary" && chmod +x "$canary"
/usr/local/bin/agent-probe > /tmp/probe-test.txt 2>&1 &
probePid=$!
sleep 2
"$canary" 0 || true
sleep 2
kill $probePid 2>/dev/null
wait $probePid 2>/dev/null
echo "CANARY=$canary"
echo "HITS=$(grep -c -- "$canary" /tmp/probe-test.txt || true)"
rm -f "$canary"
' 2>/dev/null | tr -d '\r')"

canaryPath="$(echo "$withProbe" | sed -n 's/^CANARY=//p')"
hitsWithProbe="$(echo "$withProbe" | sed -n 's/^HITS=//p')"
echo "  canário: $canaryPath"
echo "  ocorrências no registro: ${hitsWithProbe:-0}"
if [ "${hitsWithProbe:-0}" -ge 1 ]; then
  echo "  ✅ o kernel viu o execve, com o caminho COMPLETO"
else
  fail "o coletor NÃO viu o canário — a probe não está enxergando"
fi

echo
echo "=== 3/6 prova de falha: SEM o coletor, o canário não pode aparecer ==="
# Sem este sentido, o teste acima poderia estar passando por qualquer motivo —
# inclusive por um arquivo de saída antigo deixado por uma rodada anterior.
withoutProbe="$(root_ssh '
canary="/tmp/probe-canary-nao-observado-$RANDOM-$$"
cp "$(readlink -f "$(command -v sleep)")" "$canary" && chmod +x "$canary"
rm -f /tmp/probe-test-off.txt
touch /tmp/probe-test-off.txt
"$canary" 0 || true
sleep 1
echo "HITS=$(grep -c -- "$canary" /tmp/probe-test-off.txt || true)"
rm -f "$canary"
' 2>/dev/null | tr -d '\r')"

hitsWithoutProbe="$(echo "$withoutProbe" | sed -n 's/^HITS=//p')"
echo "  ocorrências sem coletor: ${hitsWithoutProbe:-0}"
if [ "${hitsWithoutProbe:-0}" -eq 0 ]; then
  echo "  ✅ sem coletor não há registro — o sinal vem da probe, não do acaso"
else
  fail "apareceu registro SEM o coletor rodando; o teste não prova nada"
fi

echo
echo "=== 4/6 o registro traz o caminho COMPLETO, não só o comm truncado? ==="
# `comm` tem 16 bytes fixos no kernel. Um coletor que guardasse só ele e se
# chamasse "o que rodou" estaria mentindo: `/usr/local/bin/agentd` viraria
# `agentd`, e `python3 /workspace/x.py` viraria `python3`.
sample="$(root_ssh 'grep -m1 "uid=" /tmp/probe-test.txt 2>/dev/null' 2>/dev/null | tr -d '\r')"
echo "  amostra: ${sample:-<vazia>}"
if echo "$sample" | grep -qE ' /[^ ]+/'; then
  echo "  ✅ o caminho absoluto está no registro"
else
  fail "o registro não traz caminho absoluto — só o comm truncado não serve"
fi

echo
echo "=== 5/6 a probe de REDE ve a conexao que sai? ==="
# A porta e asserida pelo NUMERO EXATO, e nao por "houve conexao".
#
# Isto pegou um defeito real em 31/08/2026: a primeira versao convertia a ordem
# de bytes da porta duas vezes, e o 443 saia como 47873. O teste unitario NAO
# pegou -- ele montava o registro com a mesma suposicao errada do codigo. Foi a
# execucao na maquina, olhando um numero que nao podia estar certo, que
# denunciou. Por isso a assercao e sobre o valor, nunca sobre a presenca.
network="$(root_ssh '
/usr/local/bin/agent-probe -verbose > /tmp/probe-net-test.txt 2>&1 &
probePid=$!
sleep 2
curl -s -o /dev/null --max-time 4 https://api.github.com/ 2>/dev/null || true
curl -s -o /dev/null --max-time 3 http://169.254.169.254/metadata/v1/id 2>/dev/null || true
sleep 3
kill $probePid 2>/dev/null
wait $probePid 2>/dev/null
echo "HTTPS=$(grep -c ":443 (publico)" /tmp/probe-net-test.txt || true)"
echo "META=$(grep -c "169.254.169.254:80 (PRIVADO)" /tmp/probe-net-test.txt || true)"
' 2>/dev/null | tr -d '\r')"

httpsHits="$(echo "$network" | sed -n 's/^HTTPS=//p')"
metadataHits="$(echo "$network" | sed -n 's/^META=//p')"
echo "  conexoes na porta 443: ${httpsHits:-0}"
if [ "${httpsHits:-0}" -ge 1 ]; then
  echo "  ✅ a porta sai com o numero CERTO (443, nao 47873)"
else
  fail "nenhuma conexao 443 registrada -- ou a probe nao ve, ou a ordem de bytes voltou a inverter"
fi

echo
echo "=== 6/6 destino PRIVADO e marcado como tal? ==="
# 169.254.169.254 e o endereco de metadados da nuvem: quem o alcanca le as
# credenciais da instancia. E a conexao mais interessante que esta maquina pode
# produzir, e ela precisa aparecer marcada -- senao some no meio das outras.
echo "  acessos a metadados marcados: ${metadataHits:-0}"
if [ "${metadataHits:-0}" -ge 1 ]; then
  echo "  ✅ o endereco de metadados da nuvem foi marcado como PRIVADO"
else
  fail "o acesso a 169.254.169.254 nao foi marcado -- a distincao publico/privado nao funciona"
fi

echo
if [ "$failures" -eq 0 ]; then
  echo "✅ o coletor eBPF ve o que diz ver: exec e rede  (erros: 0)"
else
  echo "🛑 o coletor NÃO está provado  (erros: $failures)"
fi
exit "$((failures > 0))"
