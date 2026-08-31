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

echo "1/5 sintaxe"
for f in scripts/*.sh nixos/scripts/*.sh; do
  bash -n "$f" 2>/dev/null || fail "$(basename "$f"): erro de sintaxe"
done
ok "todos analisam"

echo
echo "2/5 variavel referenciada e nunca atribuida (SC2154)"
if ! command -v shellcheck >/dev/null 2>&1; then
  echo "  ⚠️  shellcheck ausente — instale com: brew install shellcheck"
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
echo "3/5 quem usa a API sem carregar o token"
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
echo "4/5 suite sem trava (duas concorrentes se contaminam em silencio)"
# As suites mexem no MESMO agentd da MESMA maquina. Duas ao mesmo tempo
# produzem log entrelacado e resultado mentiroso NOS DOIS SENTIDOS -- medido em
# 30/08/2026, quando uma execucao orfa reprovou a reconciliacao de uma segunda
# por contaminacao, e um "erros: 0" de outra deu confianca em codigo nao
# exercitado. Suite nova que esqueca a trava reabre isso.
for f in scripts/08-validate.sh scripts/25-serve-integration-test.sh \
         scripts/27-privilege-test.sh scripts/32-end-to-end.sh \
         scripts/34-hostile-test.sh scripts/13-integration-test.sh \
         scripts/17-delegation-test.sh scripts/21-web-search-test.sh \
         scripts/35-connector-ssrf-test.sh scripts/36-guardrails-test.sh \
         scripts/37-redaction-test.sh; do
  [ -f "$f" ] || continue
  grep -qE '^[[:space:]]*suite_lock' "$f" || fail "$(basename "$f"): nao chama suite_lock"
done
[ "$errs" -eq 0 ] && ok "toda suite toma a trava"

echo
echo "5/5 mensagem procurada pelo teste existe no codigo"
# Teste que procura uma mensagem que o produto NAO emite reprova para sempre --
# ou, pior, passa a nunca casar e vira verificacao vazia.
#
# Aconteceu em 30/08/2026: o sweep de renomeacao para o ingles traduziu "ativa"
# -> "activeTask" DENTRO da string `grep -q "tem uma tarefa ativa"`, e o teste
# passou a reprovar "a trava nao impediu a segunda tarefa" com a trava
# perfeitamente de pe -- provada, no mesmo dia, pelo 409 da suite HTTP.
#
# Literal de string nunca se renomeia; so identificador. Este gate e a rede.
python3 - <<'PYTHON'
import pathlib, re, sys

# Todo texto Go do agente, num so lugar: a mensagem pode vir de qualquer pacote.
goText = "\n".join(
    f.read_text(errors="ignore")
    for f in pathlib.Path("agent").rglob("*.go")
)

# So `grep -q "..."` com frase (2+ palavras) e sem variavel: e a forma que
# procura mensagem do produto. Um padrao com $ ou ` e regex montada em tempo de
# execucao, e ai a comparacao literal nao vale.
pattern = re.compile(r'grep -q(?:[a-zA-Z]*) "([^"$`]{12,})"')

# Mensagens que vem do SISTEMA, e nao do agentd. Nominal e com motivo, do mesmo
# jeito que a exclusao de lib.sh mais acima: uma regra generica ("ignorar frase
# em ingles") erraria nos dois sentidos e viraria a porta pela qual o proximo
# vazamento de sweep passaria despercebido.
FROM_SYSTEM = {
    "Status: active",        # saida do systemctl
    "Failed loading yaml",   # saida do cloud-init
}
missing = []
for script in sorted(pathlib.Path("scripts").glob("*.sh")):
    for phrase in pattern.findall(script.read_text(errors="ignore")):
        if " " not in phrase.strip() or phrase.strip() in FROM_SYSTEM:
            continue
        # `grep` recebe regex: "aviso\\(s\\) pendente" e a forma escapada de
        # "aviso(s) pendente", que e o que o Go de fato emite.
        phrase = phrase.replace("\\(", "(").replace("\\)", ")")
        # Alternativa de regex: basta um dos lados existir.
        #
        # Separa por AMBAS as formas: `\|` da BRE (`grep -q`) e `|` da ERE
        # (`grep -qE`). Tratar so uma delas deixa a frase inteira ir para a
        # comparacao e produz alarme falso -- que custa igual ao falso verde,
        # e leva a mexer no que funciona.
        alternatives = [
            part.strip()
            for part in re.split(r"\\\||\|", phrase)
            if part.strip()
        ]
        ok = False
        for alt in alternatives:
            if alt in goText:
                ok = True
                break
            # Mensagem COMPOSTA: o Go emite "habilidades aplicadas: %s" e o
            # teste procura "habilidades aplicadas: estilo". Comparar o texto
            # inteiro reprovaria todas -- entao compara o prefixo ate o ":".
            #
            # Prefixo curto (< 12 chars) e generico demais para julgar:
            # "Status:" vem do systemctl, nao do agentd, e nao ha .go que o
            # emita. Julga-lo produziria alarme falso, que custa igual.
            head = alt.split(":")[0].strip()
            if len(head) >= 12 and head in goText:
                ok = True
                break
        if not ok:
            missing.append((script.name, phrase))

for name, phrase in missing:
    print(f"    {name}: nenhum .go emite \"{phrase}\"")
sys.exit(1 if missing else 0)
PYTHON
if [ $? -eq 0 ]; then
  ok "toda mensagem procurada existe no codigo"
else
  fail "ha teste procurando mensagem que o produto nao emite"
fi

echo
echo "erros: $errs"
exit $errs
