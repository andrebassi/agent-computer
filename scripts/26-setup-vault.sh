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
prep="$(agent_ssh 'test -s /etc/agentd/vault.pass && stat -c "%U:%G %a" /etc/agentd/vault.pass || echo AUSENTE' | tr -d '\r')"
case "$prep" in
  "agentd:agentd 600") echo "  ✅ senha do cofre presente, $prep" ;;
  AUSENTE) echo "  🛑 /etc/agentd/vault.pass nao existe — o droplet e anterior a esta versao do cloud-init; rode 'task update'"; exit 1 ;;
  *) echo "  🛑 permissao ou dono errados: $prep (esperado agentd:agentd 600)"; exit 1 ;;
esac

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
} | agent_ssh 'sudo -n -u agentd /workspace/agentd -vault-init -state /workspace/agent' 2>&1 | sed 's/^/  /'

echo
echo "4/4 conferindo pelo EFEITO"
# O `-catalog list` sobe o binario inteiro sem chamar o modelo nem tocar em tela
# nenhuma. Se o cofre estiver ilegivel, ele reclama aqui.
agent_ssh 'sudo -n -u agentd /workspace/agentd -catalog list 2>&1 | head -5' | sed 's/^/  /'
echo
# A prova que importa: o MODELO nao le a identidade. Roda como `agent`, que e
# exatamente o usuario para quem as ferramentas dele caem.
echo "  o usuario do modelo alcanca a identidade do cofre?"
if agent_ssh 'cat /etc/agentd/vault.pass >/dev/null 2>&1'; then
  echo "  🛑 ALCANCA — o rebaixamento nao esta protegendo nada"
  exit 1
fi
echo "  ✅ nao alcanca (permissao negada, como esperado)"
