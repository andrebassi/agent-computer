#!/bin/bash
# Prova a delegacao com a tarefa MISTA — a unica que nenhum dos dois agentes
# faz sozinho.
#
# Grok navega e le a pagina; o Claude Code escreve o codigo. Uma tarefa que so
# navegasse, ou so codificasse, nao provaria nada: provaria que um dos dois
# funciona, o que ja se sabia.
#
# O criterio de pronto e verificado AQUI, do lado de fora, rodando o teste que
# o agente diz ter escrito. Acreditar no relatorio dele seria acreditar na
# palavra do proprio testado.
#
# Python com unittest, e nao Go: o droplet nao tem toolchain de Go, e o
# `unittest` e biblioteca padrao — sobrevive ao rebuild sem entrar na lista de
# pacotes do cloud-init.
source "$(dirname "$0")/lib.sh"
source "$(dirname "$0")/suite-lock.sh"
suite_lock "$(basename "$0")"
set -euo pipefail
# Sem isto, `droplet_ip` nao consulta a API, `agent_ssh` devolve rc=1 e o `set
# -e` aborta o script SEM MENSAGEM -- ele parece ter terminado na primeira etapa.
#
# Mordeu de novo em 30/08/2026, neste arquivo: o teste de delegacao morria em
# "1/4 limpando o resultado anterior" com rc=1 e nada mais. A varredura que
# achou isto compara quem USA agent_ssh com quem CHAMA load_token, e vale a pena
# repetir sempre que um script novo entrar.
load_token

projectDir="/workspace/projects/star-count"


echo "=== 1/4 limpando o resultado anterior ==="
# Sem isto, um diretorio deixado por uma corrida antiga faria o teste passar
# sem o agente ter feito nada — o modo de falha mais silencioso possivel.
agent_ssh "rm -rf '$projectDir' && echo limpo"

echo
echo "=== 2/4 rodando a tarefa mista ==="
taskText="Abra no navegador https://api.github.com/repos/golang/go e leia o valor do campo stargazers_count. \
Depois use delegate_to_code para pedir ao agente de codigo: crie em ${projectDir} um modulo Python \
formatter.py com a funcao format_count(n) que devolve o numero com separador de milhar por ponto \
(exemplo: 1234567 vira 1.234.567), um main.py que imprime format_count do valor real que voce leu, e \
test_formatter.py com unittest cobrindo zero, tres digitos, quatro digitos e sete digitos. \
Criterio de pronto: 'python3 -m unittest discover' passar dentro de ${projectDir}. \
Ao final, diga qual foi o stargazers_count que voce leu na pagina."

# Roda como `agentd`, que le a chave do COFRE -- e por isso ela nao aparece em
# lugar nenhum.
#
# Antes era `agent_ssh "XAI_API_KEY='$xaiKey' agentd ..."`, com um comentario
# afirmando que a chave ia "no ambiente, nunca em linha de comando". O
# comentario estava errado: a atribuicao faz parte do comando remoto, e `ps` no
# droplet a mostrava. Agora nao ha chave para expor -- o binario a busca no
# cofre, e as ferramentas do modelo continuam rebaixadas para `agent`.
#
# A saida vai INTEIRA para o log, sem `| tail`: com `set -o pipefail` o `tail`
# devolveria o proprio codigo de saida, e uma falha do agente sairia como 0 —
# foi exatamente o que aconteceu na primeira corrida deste script.
agentd_run "-screen 2 -prompt \"$taskText\"" 2>&1

echo
echo "=== 3/4 verificando pelo EFEITO: o teste roda? ==="
# O teste de verdade, na maquina, e nao a afirmacao de que ele passa.
#
# A saida e CAPTURADA e inspecionada, em vez de apenas ecoada.
#
# Antes o comando terminava em `| tail -15`, e com isso o codigo de saida era o
# do `tail` -- sempre 0. O efeito medido em 30/08/2026: `python3: command not
# found` na maquina, unittest nunca rodou, e o script reportou SUCESSO. Uma
# verificacao que nao consegue rodar precisa reprovar, nao passar.
saidaTeste="$(agent_ssh "cd '$projectDir' 2>/dev/null && ls -la && echo '--- unittest ---' && python3 -m unittest discover -v 2>&1" 2>&1)"
echo "$saidaTeste" | tail -20
if echo "$saidaTeste" | grep -qE 'command not found|No such file or directory'; then
  echo "🛑 a verificacao NAO conseguiu rodar -- isso e falha, nao aprovacao"
  exit 1
fi
if ! echo "$saidaTeste" | grep -qE '^OK$|^OK \('; then
  echo "🛑 o criterio de pronto NAO foi atingido (unittest nao reportou OK)"
  exit 1
fi

echo
echo "=== 4/4 o programa imprime o numero REAL lido da web? ==="
# A secao 3 prova que o teste do agente passa. Isso ainda nao prova que ele leu
# a web: `format_count` passaria nos quatro casos com `main.py` imprimindo um
# numero inventado.
#
# Aqui o numero e comparado com a fonte, buscada DAQUI. E a unica verificacao
# que separa "o agente navegou" de "o agente chutou um numero plausivel" -- e
# chutar um numero de estrelas plausivel e exatamente o que um modelo faz bem.
saidaPrograma="$(agent_ssh "cd '$projectDir' && python3 main.py 2>&1 | head -3")"
echo "$saidaPrograma" | sed 's/^/  /'

# Digitos do que o programa imprimiu, sem os separadores.
impresso="$(printf '%s' "$saidaPrograma" | tr -cd '0-9')"
if [ -z "$impresso" ]; then
  echo "🛑 o programa nao imprimiu numero nenhum"
  exit 1
fi

# A fonte, lida daqui. Se a API do GitHub nao responder, o teste NAO aprova por
# omissao: diz que nao pode comparar, e segue sem reprovar -- indisponibilidade
# de terceiro nao e defeito do produto, mas tambem nao e prova.
real="$(timeout 30s curl -sS --max-time 20 https://api.github.com/repos/golang/go 2>/dev/null \
  | sed -n 's/.*"stargazers_count"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' | head -1)"

if [ -z "$real" ]; then
  echo "  ⚠️  a API do GitHub nao respondeu daqui -- sem comparacao possivel"
  echo "      (o numero impresso foi $impresso; confira a mao se importar)"
elif [ "$impresso" = "$real" ]; then
  echo "  ✅ bate com a fonte: $real estrelas"
else
  # Tolera diferenca pequena: estrela muda entre a leitura do agente e esta.
  diferenca=$(( impresso > real ? impresso - real : real - impresso ))
  if [ "$diferenca" -le 50 ]; then
    echo "  ✅ bate com a fonte dentro da margem: agente $impresso, agora $real"
  else
    echo "🛑 o numero NAO bate com a fonte: agente $impresso, real $real"
    echo "   diferenca de $diferenca estrelas -- alem do que o tempo explica."
    echo "   O agente provavelmente inventou em vez de ler a pagina."
    exit 1
  fi
fi
