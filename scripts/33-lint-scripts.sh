#!/bin/bash
# Gate dos scripts em shell: variavel orfa e uso de `agent_ssh` sem `load_token`.
#
# # As duas coisas que ele pega, e por que nenhuma aparece de outro jeito
#
# 1. VARIAVEL ORFA. `bash -n` conferre SINTAXE e passa direto por
#    `echo "$naoExiste"` -- so `set -u` em execucao reclama, e ai o script ja
#    esta rodando contra a maquina.
#
#    Duas apareceram em 30/08/2026, as duas sobras de renomeacao para o ingles:
#      17-delegation-test.sh  declarava `taskText`, usava `$tarefa`  -> morria
#      13-integration-test.sh declarava `taskState`, usava `$estado` -> PASSAVA,
#                             imprimindo "estado: " vazio na mensagem de sucesso
#
#    O segundo e o pior: teste verde que nao verificou o que diz verificar.
#
# 2. `agent_ssh` SEM `load_token`. O `doctl` roda sem credencial, `droplet_ip`
#    devolve vazio, e dai o efeito depende do `set -e`:
#      com `set -e`  -> o script morre SEM MENSAGEM na primeira chamada
#      com `set +e`  -> a checagem responde "nome livre" SEMPRE, inclusive com
#                       o droplet no ar
#
#    Os dois aconteceram nesta mesma sessao, em scripts diferentes.
#
# Roda no Mac, sem tocar na maquina.
set -uo pipefail

repoRoot="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repoRoot"

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

echo "1/3 sintaxe"
for f in scripts/*.sh nixos/scripts/*.sh; do
  bash -n "$f" 2>/dev/null || fail "$(basename "$f"): erro de sintaxe"
done
ok "todos analisam"

echo
echo "2/3 variavel referenciada e nunca atribuida (SC2154)"
if ! command -v shellcheck >/dev/null 2>&1; then
  echo "  ⚠️  shellcheck ausente — instale com `brew install shellcheck`"
  echo "      Sem ele esta checagem NAO roda, e a ausencia dela ja custou"
  echo "      dois testes quebrados sem ninguem perceber."
  errs=$((errs+1))
else
  achou=0
  for f in scripts/*.sh nixos/scripts/*.sh; do
    out="$(timeout 30s shellcheck -f gcc -i SC2154 "$f" 2>/dev/null)"
    if [ -n "$out" ]; then
      achou=1
      echo "$out" | sed 's/^/    /'
    fi
  done
  [ "$achou" -eq 0 ] && ok "nenhuma variavel orfa" || fail "ha variavel orfa (sobra de renomeacao)"
fi

echo
echo "3/3 quem usa a API sem carregar o token"
for f in scripts/*.sh; do
  # lib.sh DEFINE as funcoes; este arquivo as MENCIONA no padrao de busca.
  # Nenhum dos dois as chama, e a exclusao e nominal de proposito -- uma regra
  # generica de "ignorar mencao em string" erraria nos dois sentidos e viraria a
  # porta pela qual um script de verdade passaria despercebido.
  case "$(basename "$f")" in lib.sh|33-lint-scripts.sh) continue ;; esac
  usa="$(grep -cE 'agent_ssh|root_ssh|agentd_run|droplet_ip|droplet_id|agent_host' "$f")"
  # `load_token` no inicio da linha: dentro de comentario ou de string nao vale.
  tem="$(grep -cE '^[[:space:]]*load_token' "$f")"
  if [ "$usa" -gt 0 ] && [ "$tem" -eq 0 ]; then
    fail "$(basename "$f"): usa a API $usa vez(es) e nunca chama load_token"
  fi
done
[ "$errs" -eq 0 ] && ok "todos os que usam a API carregam o token"

echo
echo "erros: $errs"
exit $errs
