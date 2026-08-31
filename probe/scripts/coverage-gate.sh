#!/bin/bash
# Gate de cobertura do coletor eBPF.
#
# Separado do gate do `agent/` porque são dois módulos Go, e uma exclusão que
# valesse nos dois esconderia dívida de um atrás do outro.
#
# A EXCLUSÃO DECLARADA aqui é `collector`, e o motivo é físico, não conveniência:
# carregar programa BPF e atachar tracepoint exige um kernel Linux e privilégio,
# e o Mac onde este código é escrito não tem nem um nem outro. Testá-lo aqui
# exigiria simular a chamada de sistema `bpf()`, o que provaria que o simulador
# funciona.
#
# A corretude dele NÃO fica sem prova: ela é a camada de máquina, em
# `scripts/46-ebpf-test.sh`, com gatilho determinístico (um canário copiado de
# `/bin/true`, com nome aleatório) e prova de falha nos dois sentidos. Excluir
# aqui e dizer ONDE a prova está é diferente de abrir mão dela.
set -uo pipefail

# GOWORK=off é obrigatório: o ~/works/go.work do workspace não lista este
# módulo, e sem a variável o build falha com um erro sobre `go.work` que não tem
# nada a ver com o código.
export GOWORK=off

cd "$(dirname "$0")/.." || exit 1

# Sobrescritível para a PROVA DE FALHA do próprio gate: com o piso em 101 ele
# TEM que reprovar. Gate que nunca reprovou provavelmente não é gate.
MINIMUM="${MINIMUM:-90}"

# Pacotes cuja cobertura não é exigida, cada um com o porquê ao lado.
EXCLUDED="collector"

echo "=== testes com -race ==="
# -race porque o coletor tem duas goroutines: uma drena o kernel, a outra
# entrega em lote. É exatamente a forma de defeito que só aparece sob carga.
# Só `./internal/...`, como o gate do módulo `agent/` — `cmd/` é o ponto de
# composição, onde não há decisão para testar: um teste ali testaria o
# framework de flags. É a mesma exclusão que o outro módulo já pratica, e
# manter os dois iguais é o que impede a dívida de migrar de um para o outro.
if ! go test -race -coverprofile=cover.out ./internal/...; then
  echo "🛑 testes falharam"
  exit 1
fi

echo
echo "=== cobertura por pacote ==="
go test -cover ./internal/... 2>/dev/null | grep -E 'coverage:' | sed 's/^/  /'

# A exclusão é APLICADA, não só anunciada.
#
# Um gate que imprime "excluído" e continua contando o pacote no total é pior
# que um sem exclusão nenhuma: ele afirma uma coisa e mede outra, e quem lê a
# saída acredita na afirmação.
grep -v "/internal/${EXCLUDED}/" cover.out > cover.filtered.out
total="$(go tool cover -func=cover.filtered.out | awk '/^total:/ {gsub("%","",$3); print $3}')"
echo
echo "TOTAL: ${total}%  (piso: ${MINIMUM}%)"

# O `go test -cover` IMPRIME a cobertura e sai com 0 mesmo abaixo do piso.
# Quem confiasse no código de saída dele acharia que tem gate e não tem — é o
# defeito que a regra de cobertura descreve como o mais fácil de cometer.
if ! awk -v value="$total" -v floor="$MINIMUM" 'BEGIN { exit (value+0 >= floor+0) ? 0 : 1 }'; then
  echo "🛑 cobertura abaixo do piso"
  echo
  echo "funções menos cobertas:"
  go tool cover -func=cover.filtered.out | grep -v '100.0%' | grep -v '^total:' | head -10 | sed 's/^/  /'
  exit 1
fi

echo "  ↷ ${EXCLUDED}: excluído — exige kernel Linux e privilégio."
echo "     A prova dele é scripts/46-ebpf-test.sh, na máquina."
echo "✅ cobertura aprovada"
