#!/bin/bash
# Compila o agentd para o droplet e o instala em /workspace.
#
# Vai em /workspace, e nao em /usr/local/bin, de proposito: o binario e estado
# duravel. Um `update` destroi o droplet inteiro e remonta so o volume — se o
# binario morasse no disco do sistema, toda reconstrucao exigiria um deploy
# novo antes de a maquina voltar a servir para alguma coisa.
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

echo "1/5 gate de cobertura"
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
echo "2/5 descobrindo a arquitetura do destino"
remoteArch="$(agent_ssh 'uname -m' 2>/dev/null | tr -d '\r')"
case "$remoteArch" in
  x86_64)  goArch="amd64" ;;
  aarch64) goArch="arm64" ;;
  *) echo "🛑 arquitetura desconhecida: '$remoteArch'"; exit 1 ;;
esac
echo "  $remoteArch -> GOARCH=$goArch"

echo
echo "3/5 compilando"
binPath="/tmp/agentd-linux-$goArch"
(cd "$agentDir" && GOOS=linux GOARCH="$goArch" CGO_ENABLED=0 \
  go build -trimpath -o "$binPath" ./cmd/agentd)
ls -la "$binPath" | sed 's/^/  /'

echo
echo "4/5 enviando"
# O envio vai para um nome temporario e so depois troca de lugar: um scp que
# morre no meio deixaria /workspace/agentd truncado e inutilizavel, e a proxima
# tarefa falharia com "exec format error" em vez de "arquivo velho".
host="$(agent_host)"
timeout 180s scp -i "$SSH_KEY_FILE" \
  -o StrictHostKeyChecking=accept-new \
  -o UserKnownHostsFile="$HOME/.ssh/known_hosts" \
  "$binPath" "agent@${host}:/workspace/.agentd-novo"
agent_ssh 'chmod +x /workspace/.agentd-novo && mv /workspace/.agentd-novo /workspace/agentd'

echo
echo "5/6 reiniciando a porta HTTP, se ela estiver no ar"
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
echo "6/6 conferindo pelo EFEITO, nao pelo codigo de saida"
# `-catalog list` roda sem chave de API e sem tocar em tela nenhuma: e a prova
# mais barata de que o binario executa naquela maquina.
agent_ssh '/workspace/agentd -catalog list 2>&1 | head -20' | sed 's/^/  /'
echo
echo "rota: $(agent_route)"
