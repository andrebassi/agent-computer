#!/bin/bash
# Gate de cobertura: MEDE e REPROVA.
#
# `go test -cover` imprime o número e sai com 0 mesmo abaixo do piso — isso é
# relatório, não gate. O awk no fim é o que de fato reprova.
#
# Sobre cobertura de BRANCH: o Go não a mede nativamente, só statements. Em vez
# de fingir um número que a ferramenta não produz, a compensação é explícita:
# os testes usam tabelas cobrindo cada ramo de decisão (transições inválidas,
# motivos de bloqueio, entradas malformadas), e o domínio fica em 100%.
set -euo pipefail

MINIMO="${MINIMO:-90}"
cd "$(dirname "$0")/.."

# GOWORK=off: o go.work da raiz do workspace não lista este módulo, e sem isto
# o build falha por motivo que não tem nada a ver com o código.
export GOWORK=off

# -race é obrigatório desde que o laço passou a executar ferramentas em
# paralelo. Um gate sem ele deixa corrida de dados passar VERDE, e corrida de
# dados neste projeto não trava — faz a coisa errada em silêncio, que é o modo
# de falha que a trava de tela existe para impedir.
echo "rodando testes com cobertura e detector de corrida"
go test -race -coverprofile=cover.out -covermode=atomic ./internal/... > /tmp/agent-cover.log 2>&1 || {
  echo "🛑 testes falharam:"
  grep -E "FAIL|---" /tmp/agent-cover.log | head -20
  exit 1
}

echo
echo "cobertura por pacote:"
grep "coverage:" /tmp/agent-cover.log | sed 's/^/  /'

TOTAL="$(go tool cover -func=cover.out | awk '/^total:/ {gsub("%","",$3); print $3}')"
echo
echo "TOTAL: ${TOTAL}%  (piso: ${MINIMO}%)"

# O domínio tem exigência própria e mais dura: 100%, porque é onde moram as
# regras e ele não depende de nada para ser testado.
DOMINIO="$(go tool cover -func=cover.out | grep '/internal/domain/' | awk '{gsub("%","",$3); s+=$3; n++} END {if (n>0) printf "%.1f", s/n; else print 0}')"
echo "domínio: ${DOMINIO}%  (exigência: 100%)"

# --- cobertura POR PACOTE ---------------------------------------------------
#
# A rule 06 exige adapters acima de 90%, e o gate só cobrava o TOTAL: nove
# pacotes estavam abaixo do piso sem que nada apontasse. Média alta esconde
# pacote fraco, e dívida que ninguém vê não é paga.
#
# Aqui ela AVISA por padrão e REPROVA com STRICT_PACKAGES=1. Ligar a reprovação
# hoje deixaria o gate vermelho de forma permanente, e gate sempre vermelho é
# gate desligado na prática — a passagem para estrito é uma linha, quando os
# pacotes subirem.
#
# EXCLUSÕES DECLARADAS, com o motivo ao lado (rule 15: exclusão se declara, não
# se esconde):
#
#   secret  o caminho principal de `Prompt` exige um TTY de verdade: ele começa
#           por `term.IsTerminal(fd)` e desiste com pipe ou arquivo. Testá-lo
#           pede um PTY, cuja abertura difere entre Darwin e Linux. O que NÃO
#           depende de terminal (`promptMessage`, o construtor, a recusa fora de
#           terminal) está em 100%.
COVERAGE_EXCLUDED="secret"

echo
abaixo=0
while read -r pacote valor; do
  nome="${pacote##*/}"      # ultimo segmento: journal, tools, api…
  case " $COVERAGE_EXCLUDED " in
    *" $nome "*)
      printf "  ↷ %-12s %5s%%  excluído: exige TTY (ver comentário no gate)\n" "$nome" "$valor"
      continue
      ;;
  esac
  awk -v v="$valor" -v m="$MINIMO" 'BEGIN { exit (v+0 >= m+0) ? 0 : 1 }' || {
    printf "  ⚠️  %-12s %5s%%  abaixo do piso\n" "$nome" "$valor"
    abaixo=$((abaixo + 1))
  }
done < <(grep "coverage:" /tmp/agent-cover.log \
  | awk '{
      # O nome do pacote e o percentual estao em campos SEPARADOS por tabulacao,
      # e a coluna "(cached)" aparece so as vezes -- por isso a varredura por
      # campo, e nao um sed posicional (que capturava "adap" e lia "(cached)"
      # como valor).
      pkg = ""; cov = "";
      for (i = 1; i <= NF; i++) {
        if ($i ~ /internal\//) { pkg = $i; sub(/.*internal\//, "", pkg) }
        if ($i == "coverage:") { cov = $(i+1); sub(/%$/, "", cov) }
      }
      if (pkg != "" && cov != "") print pkg, cov
    }' \
  | grep -vE '^(domain|secretref) ')

if [ "$abaixo" -gt 0 ]; then
  echo "  → $abaixo pacote(s) abaixo de ${MINIMO}% (rule 06 pede adapters > 90%)"
else
  echo "  ✅ todo pacote no piso"
fi

rc=0
if [ "${STRICT_PACKAGES:-0}" = "1" ] && [ "$abaixo" -gt 0 ]; then
  echo "🛑 STRICT_PACKAGES=1: pacote abaixo do piso reprova"
  rc=1
fi

awk -v t="$TOTAL" -v m="$MINIMO" 'BEGIN { exit (t+0 >= m+0) ? 0 : 1 }' || {
  echo "🛑 cobertura total abaixo do piso"
  echo
  echo "funções menos cobertas:"
  go tool cover -func=cover.out | awk '$3+0 < 90 && $3 ~ /%/ {print "  " $0}' | head -10
  rc=1
}
awk -v d="$DOMINIO" 'BEGIN { exit (d+0 >= 100) ? 0 : 1 }' || {
  echo "🛑 domínio abaixo de 100%"
  rc=1
}

[ "$rc" -eq 0 ] && echo "✅ cobertura aprovada"
exit $rc
