#!/bin/bash
# Provisiona o cofre do droplet com os segredos lidos do `pass` do Mac.
#
# # O desenho, em uma tela
#
#   Mac                          droplet
#   ---                          -------
#   pass show <chave>    -SSH->  agentd -vault-init   (roda como agentd)
#                                  |
#                                  +-- /workspace/agent/vault/   cifrado, no volume
#                                  +-- /etc/agentd/gopass/       identidade age, no sistema
#
# Duas propriedades que o desenho compra, e um limite honesto:
#
#   1. A foto do volume (`task snapshot`) para de carregar segredo em claro.
#      Ela vai para a conta do DigitalOcean, e hoje levava a chave da xAI
#      legivel por quem tivesse o token da conta.
#   2. O MODELO nao alcanca o cofre. As ferramentas dele caem para o usuario
#      `agent`, e a identidade e 0600 do `agentd`.
#   3. Limite: quem for root na maquina le tudo. Cofre e cifra em repouso e
#      separacao de usuario, nao isolamento contra root.
#
# # Por que a criacao e em Go, e nao aqui
#
# A biblioteca do gopass so ABRE store existente -- criar exige o CLI, que esta
# de proposito fora do droplet (o binario estatico unico e o modelo de deploy).
# Entao quem cria e o proprio `agentd`: o mesmo binario que le e o que escreve,
# e o formato nao tem como divergir entre os dois lados.
#
# # Por que por ROOT, e nao por `sudo -u agentd`
#
# Havia uma regra `agent ALL=(agentd) NOPASSWD: agentd -vault-init*`, e ela foi
# REMOVIDA de proposito: operador e modelo sao o mesmo usuario `agent`, entao
# toda concessao dada ao operador e dada ao modelo junto -- e essa deixava o
# modelo gravar no cofre e cadastrar conector.
#
# A autoridade do operador e a chave SSH de root, que existe so no Mac. O
# `agentd_run` entra por ela e desce para `agentd` com `setpriv`, sem depender
# de linha nenhuma em sudoers.
#
# A inconsistencia so apareceu na maquina NixOS: o droplet Ubuntu ainda tinha o
# sudoers antigo aplicado a mao, entao o script funcionava ali por acidente.
#
# # Os valores nunca aparecem
#
# Nem em argumento (`ps` mostra a linha de comando de qualquer processo a
# qualquer usuario da maquina), nem em log, nem em arquivo temporario no
# droplet. Viajam pela ENTRADA PADRAO do SSH, direto para o binario.
source "$(dirname "$0")/lib.sh"
set -euo pipefail
# Sem isto, `droplet_ip` nao consulta a API, `agent_ssh` devolve rc=1 e o `set
# -e` aborta o script SEM MENSAGEM -- ele parece ter terminado na etapa 1.
load_token

# Mapa: chave no cofre do droplet <- entrada no `pass` do Mac.
#
# Os nomes do lado do cofre sao contrato com o binario (cmd/agentd/vault.go).
# Um nome digitado diferente aqui produz "segredo ausente" com o segredo ali,
# gravado sob outro nome -- falha que manda procurar no lugar errado.
declare -a mapping=(
  "agent/xai/apikey=bassi/xai/apikey"
  # O token da porta HTTP tambem vem daqui.
  #
  # O arquivo /workspace/agent/api-token continua existindo, mas so para o LADO
  # CLIENTE: ele e `agent:agent 0600`, e depois da separacao de usuario o
  # `agentd` nao o le -- o servico morria com "permission denied" num arquivo
  # que existia. O servidor le do cofre; o operador le do arquivo.
  "agent/http/token=bassi/agent-computer/api-token"
)

echo "1/4 conferindo o cofre local"
missing=0
for pair in "${mapping[@]}"; do
  passEntry="${pair#*=}"
  if ! timeout 25s pass show "$passEntry" >/dev/null 2>&1; then
    echo "  🛑 ausente no pass: $passEntry"
    missing=$((missing+1))
  else
    echo "  ✅ $passEntry"
  fi
done
[ "$missing" -gt 0 ] && { echo "🛑 destrave o GPG ou grave as entradas faltantes"; exit 1; }

echo
echo "2/4 conferindo o preparo da maquina"
# A senha do cofre e gerada NO DROPLET pelo cloud-init e nunca transportada.
# Sem ela o binario recusa a criacao, e a mensagem dele ja diz isto -- mas
# conferir aqui poupa uma viagem e da um erro mais proximo da causa.
#
# ATENCAO: a conferencia vai por SSH de ROOT, e nao como `agent`.
#
# Custou um falso negativo descobrir: /etc/agentd e 0700 do agentd, entao o
# `test -s` rodando como `agent` recebia "permission denied" e o script relatava
# "vault.pass nao existe" com o arquivo la. O verificador acusava de ausencia o
# que era a protecao funcionando -- e mandava rodar `task update` a toa.
#
# A licao vale alem deste script: verificacao que roda com o usuario RESTRITO
# nao consegue distinguir "nao existe" de "nao posso ver".
rootHost="$(agent_host)"
prep="$(timeout 30s ssh -i "$SSH_KEY_FILE" \
  -o StrictHostKeyChecking=accept-new \
  -o UserKnownHostsFile="$HOME/.ssh/known_hosts" \
  "root@${rootHost}" 'test -s /etc/agentd/vault.pass && stat -c "%U:%G %a" /etc/agentd/vault.pass || echo AUSENTE' | tr -d '\r')"
case "$prep" in
  "agentd:agentd 600") echo "  ✅ senha do cofre presente, $prep" ;;
  AUSENTE) echo "  🛑 /etc/agentd/vault.pass nao existe — o droplet e anterior a esta versao do cloud-init; rode 'task update'"; exit 1 ;;
  *) echo "  🛑 permissao ou dono errados: $prep (esperado agentd:agentd 600)"; exit 1 ;;
esac

echo
echo "2b/4 o cofre existente ainda ABRE com a identidade desta maquina?"
# Detecta o cofre ORFAO -- store cifrado para uma chave que nao existe mais.
#
# Acontece toda vez que a maquina e reconstruida: a identidade age mora em
# /etc/agentd, no disco do SISTEMA, e isso e deliberado (e o que faz a foto do
# volume ser inutil sozinha). O store fica no volume, sobrevive, e passa a estar
# cifrado para uma chave destruida.
#
# O sintoma engana: `-vault-init` GRAVA sem reclamar (ele cifra para os
# destinatarios que achou no store), e so a LEITURA falha -- o servico sobe e
# morre com "cofre ilegivel". Escrever num cofre que nao se le e o pior desfecho
# possivel, porque parece ter funcionado.
#
# Por isso a checagem e `-vault-check`, que LE de verdade. A primeira versao
# usava `-catalog list` e passava sempre: aquele comando lista conectores e nao
# toca no cofre -- uma verificacao que nao exercita o que verifica.
#
# Recriar nao perde nada: o cofre e DERIVADO do `pass`, nao a origem. Todo
# segredo daqui e regravado logo abaixo.
if ! agentd_run '-vault-check -state /workspace/agent' >/dev/null 2>&1; then
  echo "  o cofre nao abre com a identidade atual (maquina reconstruida)"
  echo "  recriando -- o conteudo vem do pass, entao nada se perde"
  root_ssh 'rm -rf /workspace/agent/vault && install -d -m 0700 -o agentd -g agentd /workspace/agent/vault'
  echo "  cofre zerado"
else
  echo "  ✅ o cofre existente abre normalmente"
fi

echo
echo "3/4 gravando no cofre"
# O laco monta `chave=valor` linha a linha e entrega TUDO de uma vez pela
# entrada padrao. Uma invocacao por segredo custaria uma derivacao scrypt cada,
# que e cara de proposito.
{
  for pair in "${mapping[@]}"; do
    vaultKey="${pair%%=*}"
    passEntry="${pair#*=}"
    printf '%s=%s\n' "$vaultKey" "$(timeout 25s pass show "$passEntry" | head -1)"
  done
} | agentd_run '-vault-init -state /workspace/agent' 2>&1 | sed 's/^/  /'

echo
echo "4/4 conferindo pelo EFEITO"
# O `-catalog list` sobe o binario inteiro sem chamar o modelo nem tocar em tela
# nenhuma. Se o cofre estiver ilegivel, ele reclama aqui.
agentd_run '-catalog list 2>&1 | head -5' | sed 's/^/  /'
echo
# A prova que importa: o MODELO nao le a identidade. Roda como `agent`, que e
# exatamente o usuario para quem as ferramentas dele caem.
echo "  o usuario do modelo alcanca a identidade do cofre?"
if agent_ssh 'cat /etc/agentd/vault.pass >/dev/null 2>&1'; then
  echo "  🛑 ALCANCA — o rebaixamento nao esta protegendo nada"
  exit 1
fi
echo "  ✅ nao alcanca (permissao negada, como esperado)"
