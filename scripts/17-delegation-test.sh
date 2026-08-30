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
set -euo pipefail

projectDir="/workspace/projects/star-count"

xaiKey="$(timeout 25s pass show bassi/xai/apikey 2>/dev/null | head -1)"
[ -z "$xaiKey" ] && { echo "🛑 chave da xAI nao disponivel"; exit 1; }

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

# A chave vai no AMBIENTE, nunca em linha de comando: `ps` a exporia a qualquer
# processo da maquina.
#
# E a saida vai INTEIRA para o log, sem `| tail`: com `set -o pipefail` o `tail`
# devolveria o proprio codigo de saida, e uma falha do agente sairia como 0 —
# foi exatamente o que aconteceu na primeira corrida deste script.
agent_ssh "XAI_API_KEY='$xaiKey' /usr/local/bin/agentd -screen 2 -prompt \"$tarefa\"" 2>&1

echo
echo "=== 3/4 verificando pelo EFEITO: o teste roda? ==="
# O teste de verdade, na maquina, e nao a afirmacao de que ele passa.
agent_ssh "cd '$projectDir' 2>/dev/null && ls -la && echo '--- unittest ---' && python3 -m unittest discover -v 2>&1 | tail -15" \
  || { echo "🛑 o criterio de pronto NAO foi atingido"; exit 1; }

echo
echo "=== 4/4 o programa imprime o numero lido da web? ==="
agent_ssh "cd '$projectDir' && python3 main.py 2>&1 | head -3"
