#!/bin/bash
# Compila o agentd e o instala em /usr/local/bin do droplet, como root.
#
# ⚠️ MUDOU DE LUGAR. Morava em /workspace, tratado como estado duravel para
# sobreviver ao `update`. A revisao de seguranca mostrou o custo dessa
# conveniencia: /workspace e do usuario `agent`, e e como `agent` que rodam as
# ferramentas do MODELO. Ele substituiria o binario do servico -- que roda como
# `agentd`, dono do cofre. Quem escreve o binario e dono do servico.
#
# Agora e root:root 0755 no disco do sistema, e a escrita so acontece por SSH de
# root, cuja chave existe apenas no Mac. Em troca, `update` leva o binario junto
# e pede `task deploy` depois.
#
# O gate de cobertura roda ANTES do envio. Um binario que sobe sem teste passar
# e o jeito mais rapido de descobrir na producao o que o `go test` diria em
# cinco segundos.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
# Sem isto, `droplet_ip` nao consulta a API, `agent_ssh` devolve rc=1 e o `set
# -e` aborta o script SEM MENSAGEM -- ele parece ter terminado na etapa 2.
load_token

repoRoot="$(cd "$(dirname "$0")/.." && pwd)"
agentDir="$repoRoot/agent"

# GOWORK=off: o go.work da raiz do workspace nao lista este modulo, e sem isto
# o build falha por motivo que nao tem nada a ver com o codigo.
export GOWORK=off

echo "1/7 gate de cobertura"
"$agentDir/scripts/coverage-gate.sh" >/tmp/agent-deploy-cover.log 2>&1 || {
  echo "🛑 o gate reprovou — o binario NAO sobe:"
  tail -20 /tmp/agent-deploy-cover.log
  exit 1
}
tail -3 /tmp/agent-deploy-cover.log | sed 's/^/  /'

# A arquitetura vem da maquina, e nao de um palpite: o droplet e amd64 hoje,
# mas trocar de plano ou de regiao pode mudar isso, e um binario da arquitetura
# errada falha com "cannot execute binary file" — mensagem que nao aponta para
# a causa.
echo
echo "2/7 descobrindo a arquitetura do destino"
remoteArch="$(agent_ssh 'uname -m' 2>/dev/null | tr -d '\r')"
case "$remoteArch" in
  x86_64)  goArch="amd64" ;;
  aarch64) goArch="arm64" ;;
  *) echo "🛑 arquitetura desconhecida: '$remoteArch'"; exit 1 ;;
esac
echo "  $remoteArch -> GOARCH=$goArch"

echo
echo "3/7 compilando"
binPath="/tmp/agentd-linux-$goArch"
(cd "$agentDir" && GOOS=linux GOARCH="$goArch" CGO_ENABLED=0 \
  go build -trimpath -o "$binPath" ./cmd/agentd)
ls -la "$binPath" | sed 's/^/  /'

echo
echo "4/7 enviando"
#
# ⚠️ VAI POR SSH DE ROOT, e o destino e /usr/local/bin -- nao /workspace.
#
# Custou uma escalada completa descobrir por que: com o binario em /workspace,
# dono `agent`, o MODELO o substituiria. Como a regra de sudoers permite ao
# operador rodar esse caminho como `agentd`, e como o proprio servico executa
# esse arquivo, quem escreve o binario e dono do servico -- e do cofre junto.
#
# Enviar como `agent` e depois mover com sudo nao resolveria: o conteudo ainda
# teria vindo de um arquivo que o modelo controla. A escrita precisa acontecer
# por um caminho que ele nao alcanca, e a chave de root so existe no Mac.
#
# Preco: `update` reconstroi o disco do sistema e leva o binario junto, entao
# todo update pede um deploy depois. E o preco certo.
#
# O envio vai para um nome temporario e so depois troca de lugar: um scp que
# morre no meio deixaria o binario truncado, e a proxima tarefa falharia com
# "exec format error" em vez de "arquivo velho".
host="$(agent_host)"
timeout 180s scp -i "$SSH_KEY_FILE" \
  -o StrictHostKeyChecking=accept-new \
  -o UserKnownHostsFile="$HOME/.ssh/known_hosts" \
  "$binPath" "root@${host}:/root/.agentd-novo"
timeout 60s ssh -i "$SSH_KEY_FILE" \
  -o StrictHostKeyChecking=accept-new \
  -o UserKnownHostsFile="$HOME/.ssh/known_hosts" \
  "root@${host}" 'install -o root -g root -m 0755 /root/.agentd-novo /usr/local/bin/agentd && rm -f /root/.agentd-novo'

echo
echo "4b/7 conferindo que o modelo NAO escreve o binario do servico"
# Prova por comportamento, nao por leitura da permissao: o usuario para quem as
# ferramentas do modelo caem tenta escrever, e precisa levar "permission denied".
if agent_ssh 'test -w /usr/local/bin/agentd' 2>/dev/null; then
  echo "  🛑 o usuario agent ESCREVE o binario — a separacao nao esta de pe"
  exit 1
fi
agent_ssh 'stat -c "  %n: %U:%G %a" /usr/local/bin/agentd'

echo
echo "5/7 reiniciando a porta HTTP, se ela estiver no ar"
# Sem isto, o binario novo fica no disco e o SERVICO CONTINUA RODANDO O VELHO —
# o deploy reporta sucesso e nada muda no comportamento, que e o modo de falha
# mais confuso possivel: o codigo esta certo, o teste passa, e a maquina insiste
# no bug que voce acabou de corrigir.
#
# O restart e limpo: o encerramento cancela as tarefas em voo, elas gravam o
# estado e soltam a trava.
if agent_ssh "systemctl is-active --quiet agentd-api" 2>/dev/null; then
  agent_ssh "sudo systemctl restart agentd-api && sleep 2 && systemctl is-active agentd-api" \
    | sed 's/^/  /'
  agent_ssh "timeout 10s curl -sS --max-time 5 http://127.0.0.1:8787/health" | sed 's/^/  health: /'
else
  echo "  a porta nao esta no ar (nada a reiniciar)"
fi

echo
echo "6/7 conferindo pelo EFEITO, nao pelo codigo de saida"
# `-catalog list` roda sem chave de API e sem tocar em tela nenhuma: e a prova
# mais barata de que o binario executa naquela maquina.
agent_ssh '/usr/local/bin/agentd -catalog list 2>&1 | head -20' | sed 's/^/  /'
echo
echo "rota: $(agent_route)"
