#!/bin/bash
# Trava que impede DUAS suites de rodarem contra a mesma maquina ao mesmo tempo.
#
# # O defeito que ela fecha, e por que ele e do tipo pior
#
# Medido em 30/08/2026. Uma execucao anterior de `25-serve-integration-test.sh`
# ficou orfa e continuou viva; uma segunda subiu por cima. As duas escreveram no
# MESMO arquivo de log e mexeram no MESMO agentd. O resultado:
#
#   * o log saiu entrelacado, com DOIS "erros:" -- um 0 e um 1;
#   * uma secao falhou com "Failed to connect to 127.0.0.1 port 8787", porque a
#     outra instancia reiniciou o servico no meio;
#   * a secao de reconciliacao apos kill -9 reprovou por contaminacao, nao por
#     defeito do produto.
#
# Isto e pior que uma falha franca: o verde e o vermelho ficam ambos sem valor.
# Um "erros: 1" mentiroso manda procurar bug onde nao ha, e um "erros: 0" vindo
# da execucao errada da confianca em codigo que ninguem exercitou.
#
# # Por que a trava fica no MAC, e nao na maquina
#
# A corrida e entre dois processos daqui. Travar la resolveria tarde -- os dois
# ja teriam aberto SSH e mexido no estado. Aqui o segundo nem comeca.
#
# # Uso
#
#   source "$(dirname "$0")/suite-lock.sh"
#   suite_lock "$(basename "$0")"
#
# A trava e liberada sozinha na saida do script (`trap EXIT`), inclusive quando
# ele morre por `set -e` ou por sinal.

# Trava por MAQUINA, nao por script: duas suites diferentes contra o mesmo
# droplet se atrapalham do mesmo jeito que duas iguais.
#
# Fica no cache do usuario, e nao no repositorio -- e estado efemero de
# execucao, que nao se versiona.
SUITE_LOCK_DIR="${SUITE_LOCK_DIR:-$HOME/.cache/agent-computer}"
SUITE_LOCK_FILE="${SUITE_LOCK_FILE:-$SUITE_LOCK_DIR/suite.lock}"

# Toma a trava exclusiva, ou explica quem esta com ela e sai.
#
# Recebe o nome de quem esta pedindo, para a mensagem do proximo dizer QUAL
# suite esta rodando -- "ocupado" sem o nome manda a pessoa procurar no `ps`.
suite_lock() {
  local requester="${1:-desconhecido}"
  mkdir -p "$SUITE_LOCK_DIR"

  # `noclobber` e a primitiva atomica que funciona igual em macOS e Linux.
  # `flock` nao existe no macOS, e testar-depois-criar tem janela de corrida --
  # justamente a corrida que este arquivo existe para fechar.
  if ! (set -o noclobber; printf '%s pid=%s inicio=%s\n' \
        "$requester" "$$" "$(date +%H:%M:%S)" > "$SUITE_LOCK_FILE") 2>/dev/null; then
    echo "🛑 outra suite ja esta rodando contra esta maquina:" >&2
    sed 's/^/     /' "$SUITE_LOCK_FILE" >&2
    echo >&2
    echo "   Duas suites concorrentes produzem log entrelacado e resultado" >&2
    echo "   mentiroso nos dois sentidos. Espere a outra terminar." >&2
    echo >&2
    echo "   Se a dona da trava morreu sem soltar:" >&2
    echo "     rm -f $SUITE_LOCK_FILE" >&2
    return 1 2>/dev/null || exit 1
  fi

  # Solta na saida, aconteca o que acontecer -- inclusive `set -e` e Ctrl+C.
  trap 'rm -f "$SUITE_LOCK_FILE"' EXIT
}
