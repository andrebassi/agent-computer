#!/bin/bash
# Prova, NA MAQUINA, que o modelo nao alcanca o cofre nem vira root.
#
# Existe porque a separacao de privilegio nao e verificavel por leitura de
# codigo: ela depende de dono de arquivo, modo, sudoers e ordem de diretivas do
# systemd. Cada um deles falha em silencio -- a maquina sobe, tudo funciona, e a
# protecao nao esta la.
#
# # O que este script E
#
# Um teste ADVERSARIAL rodando como `agent`, que e exatamente o usuario para
# quem as ferramentas do modelo caem. Cada secao tenta a escalada de verdade e
# exige que ela FALHE. Uma secao que passe silenciosamente e uma protecao
# ausente.
#
# # O que este script NAO cobre
#
# Root na maquina le tudo. Cofre e cifra em repouso mais separacao de usuario,
# nao isolamento contra root. Quem tem a chave SSH de root do Mac contorna tudo
# isto por desenho -- e e assim que o deploy funciona.
#
# Nao aborta na primeira falha: soma os erros, para uma execucao mostrar TODAS
# as brechas em vez de uma por vez.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

# tryAsModel roda um comando com o usuario do modelo e diz se ele FUNCIONOU.
#
# Sucesso aqui e falha de seguranca: toda chamada abaixo espera rc diferente de
# zero. A saida vai para /dev/null porque algumas destas tentativas imprimiriam
# o proprio segredo se passassem.
tryAsModel() { agent_ssh "$1" >/dev/null 2>&1; }

echo "verificando a separacao de privilegio em $(droplet_ip)"

echo
echo "=== 1. os dois usuarios existem e sao distintos ==="
ids="$(agent_ssh 'id -u agent 2>/dev/null; id -u agentd 2>/dev/null' | tr -d '\r' | tr '\n' ' ')"
read -r uidAgent uidAgentd <<< "$ids"
if [ -n "${uidAgent:-}" ] && [ -n "${uidAgentd:-}" ] && [ "$uidAgent" != "$uidAgentd" ]; then
  ok "agent=$uidAgent, agentd=$uidAgentd (distintos)"
else
  fail "usuarios ausentes ou iguais: '$ids'"
fi

echo
echo "=== 2. o servico roda como agentd, nao como agent ==="
runAs="$(agent_ssh "systemctl show agentd-api -p User --value 2>/dev/null" | tr -d '\r')"
[ "$runAs" = "agentd" ] && ok "agentd-api roda como $runAs" || fail "agentd-api roda como '${runAs:-nada}', esperado agentd"

echo
echo "=== 3. o modelo NAO le a senha nem a identidade do cofre ==="
for target in /etc/agentd/vault.pass /etc/agentd/gopass; do
  if tryAsModel "cat $target 2>/dev/null || ls $target 2>/dev/null"; then
    fail "o usuario do modelo alcanca $target"
  else
    ok "$target fora de alcance"
  fi
done

echo
echo "=== 4. o modelo NAO le o cofre cifrado ==="
if tryAsModel 'ls /workspace/agent/vault'; then
  fail "o usuario do modelo lista /workspace/agent/vault"
else
  ok "/workspace/agent/vault fora de alcance"
fi

echo
echo "=== 5. o modelo NAO vira root por sudo aberto ==="
# O NOPASSWD:ALL antigo tornava tudo o resto decorativo.
#
# A escalada pelo gerenciador de pacotes muda de nome conforme o sistema, e
# testar o errado passa por engano: `apt-get` nao existe em NixOS, entao a
# tentativa falharia por "comando nao encontrado" e o teste leria isso como
# "recusado" -- aprovando sem ter exercitado nada.
osName="$(agent_os)"
echo "  sistema: $osName"
case "$osName" in
  nixos)  packageEscalation=('sudo -n nix-env -iA nixpkgs.cowsay' 'sudo -n nixos-rebuild switch') ;;
  ubuntu) packageEscalation=('sudo -n apt-get install -y cowsay') ;;
  *)      packageEscalation=() ; fail "sistema desconhecido: nao sei qual gerenciador de pacotes testar" ;;
esac
for attempt in 'sudo -n cat /etc/agentd/vault.pass' 'sudo -n -u agentd cat /etc/agentd/vault.pass' 'sudo -n bash -c id' 'sudo -n su - root -c id' 'sudo -n chown agent /etc/agentd/vault.pass' "${packageEscalation[@]}"; do
  if tryAsModel "$attempt"; then
    fail "PASSOU: $attempt"
  else
    ok "recusado: $attempt"
  fi
done

echo
echo "=== 6. o modelo NAO escreve o binario do servico ==="
# Quem escreve o binario e dono do servico -- e o servico e dono do cofre.
if tryAsModel 'test -w /usr/local/bin/agentd || cp /bin/true /usr/local/bin/agentd'; then
  fail "o usuario do modelo escreve /usr/local/bin/agentd"
else
  ok "/usr/local/bin/agentd nao e escrivel pelo modelo"
fi

echo
echo "=== 7. o modelo NAO reescreve as proprias regras ==="
# habilidades sao a instrucao dele; conectores sao o alcance de rede dele.
for target in /workspace/agent/skills /workspace/agent/connectors; do
  if tryAsModel "touch $target/teste-de-escrita"; then
    fail "o usuario do modelo escreve em $target"
    agent_ssh "rm -f $target/teste-de-escrita" >/dev/null 2>&1
  else
    ok "$target somente leitura para o modelo"
  fi
done

echo
echo "=== 8. o modelo NAO desliga o proprio rebaixamento ==="
# A escalada era: escrever AGENTD_TOOL_USER=off num EnvironmentFile que vem
# DEPOIS do Environment= fixo, porque para o systemd o ultimo vence.
if tryAsModel 'echo AGENTD_TOOL_USER=off >> /etc/agentd/xai.env'; then
  fail "o usuario do modelo escreve o EnvironmentFile do servico"
else
  ok "o EnvironmentFile esta fora de alcance"
fi
# E, mesmo que estivesse, a diretiva precisa proteger -- e o COMO muda com o
# sistema.
#
# No Ubuntu ha um EnvironmentFile, e o que protege e a ORDEM: o systemd faz o
# ultimo vencer, entao a linha fixa tem de vir depois do arquivo.
#
# No NixOS nao existe EnvironmentFile nenhum -- o valor e parte da expressao que
# cria a unidade. Isso e ESTRITAMENTE MAIS FORTE: nao ha arquivo para
# sobrescrever, e nao ha ordem para errar. Conferir "ordem" ali reprovaria a
# configuracao mais segura das duas.
unitText="$(agent_ssh 'systemctl cat agentd-api 2>/dev/null' | tr -d '\r')"
if [ "$osName" = "nixos" ]; then
  if echo "$unitText" | grep -q 'EnvironmentFile='; then
    fail "o NixOS nao deveria ter EnvironmentFile: ha um arquivo capaz de sobrescrever"
  else
    ok "sem EnvironmentFile: o valor esta na propria unidade, nao ha o que sobrescrever"
  fi
  echo "$unitText" | grep -q 'AGENTD_TOOL_USER=agent' \
    && ok "AGENTD_TOOL_USER fixado na unidade" \
    || fail "AGENTD_TOOL_USER ausente da unidade"
else
  order="$(echo "$unitText" | grep -n 'EnvironmentFile=\|Environment=AGENTD_TOOL_USER')"
  fileLine="$(echo "$order" | grep 'EnvironmentFile=' | head -1 | cut -d: -f1)"
  varLine="$(echo "$order" | grep 'Environment=AGENTD_TOOL_USER' | head -1 | cut -d: -f1)"
  if [ -n "$fileLine" ] && [ -n "$varLine" ] && [ "$varLine" -gt "$fileLine" ]; then
    ok "Environment= vem depois do EnvironmentFile= (a linha fixa vence)"
  else
    fail "ordem errada: EnvironmentFile na linha ${fileLine:-?}, Environment na ${varLine:-?}"
  fi
fi

echo
echo "=== 9. a unidade de avisos nao passa por shell ==="
# O destino vinha de arquivo que o modelo escreve, interpolado entre aspas
# dentro de `sh -c` -- um valor fechando a aspa emendaria outro comando.
execStart="$(agent_ssh "systemctl show agentd-notify -p ExecStart --value 2>/dev/null" | tr -d '\r')"
if echo "$execStart" | grep -qE '/bin/sh|/bin/bash'; then
  fail "a unidade ainda passa por shell: $execStart"
else
  ok "sem shell na unidade de avisos"
fi

echo
echo "=== 10. o operador AINDA consegue operar ==="
# Endurecer sem quebrar a operacao: se a lista fechada derrubar os comandos que
# os scripts usam, a correcao e inutil na pratica.
#
# ATENCAO: aqui NAO se pode olhar o codigo de saida.
#
# Custou um falso positivo descobrir: `systemctl is-active` devolve rc diferente
# de zero quando a unidade esta parada, e o teste leu isso como "sudo recusou".
# Ele reportou "a operacao QUEBROU" com a permissao intacta -- e a correcao
# aponta para o sudoers, que estava certo.
#
# O que distingue as duas coisas e a MENSAGEM do sudo, nao o rc: comando negado
# imprime "not allowed to execute" ou pede senha. Estado do servico nao imprime
# nenhum dos dois.
sudoRefused() {
  local output
  output="$(agent_ssh "$1" 2>&1)"
  echo "$output" | grep -qiE 'not allowed to execute|a (terminal|password) is required|Sorry, user'
}
# `ufw` so existe no Ubuntu; em NixOS a leitura equivalente e o `nft`.
# `nft` NAO existe em NixOS com networking.nftables desligado, que e o padrao --
# o backend e iptables. Testar o comando errado reprova uma maquina correta.
case "$osName" in
  nixos)  firewallRead='sudo -n iptables -S' ;;
  *)      firewallRead='sudo -n ufw status' ;;
esac
for allowed in 'sudo -n systemctl is-active agentd-api' 'sudo -n systemctl daemon-reload' "$firewallRead" 'sudo -n mount -a'; do
  if sudoRefused "$allowed"; then
    fail "a operacao QUEBROU: $allowed"
  else
    ok "permitido, como esperado: $allowed"
  fi
done

echo
echo "=== 11. a lista fechada REPROVA de verdade ==="
# Prova que a secao 10 nao esta apenas aprovando tudo. Sem este par, um
# sudoRefused quebrado faria as duas secoes passarem em silencio.
if sudoRefused 'sudo -n systemctl edit agentd-api'; then
  ok "recusado, como esperado: systemctl edit (injetaria ExecStart)"
else
  fail "PASSOU: systemctl edit — a lista fechada nao esta fechando"
fi

echo
echo "erros: $errs"
exit $errs
