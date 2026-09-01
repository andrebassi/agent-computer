#!/usr/bin/env bash
#
# Prova que o cloud-init DA NUVEM entende o user-data comprimido.
#
# # Por que um droplet de verdade, e nao um teste local
#
# Decodificar o base64 aqui no Mac prova que o gerador esta correto. NAO prova
# que o cloud-init do provedor aceita `encoding: gz+b64`, que o DigitalOcean nao
# corrompe o payload no caminho da API, nem que o arquivo chega ao disco com o
# conteudo certo. Sao tres elos, e nenhum deles roda nesta maquina.
#
# Em 31/08/2026 o caminho de criacao quebrou em producao porque a unica
# verificacao era local: o `task update` destruiu o droplet antigo e a API
# recusou o novo, com a maquina ja inexistente. Este script existe para que a
# proxima mudanca no formato do user-data seja exercitada ANTES disso.
#
# # O que ele NAO faz
#
# Nao toca no droplet `agent-computer` nem no volume duravel. Cria uma maquina
# separada, com nome proprio, a destroi no fim -- inclusive se falhar no meio.
#
#   ./scripts/48-cloudinit-roundtrip-test.sh

set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=scripts/lib.sh
source scripts/lib.sh
set +e

load_token

# Nome proprio e tamanho minimo: este droplet existe por minutos e so precisa
# bootar o cloud-init. O `s-1vcpu-1gb` custa fracao de centavo pelo periodo.
probeName="agent-computer-cloudinit-probe"
probeSize="s-1vcpu-1gb"
errors=0

fail() { echo "  🛑 $1"; errors=$((errors + 1)); }
ok()   { echo "  ✅ $1"; }

# shellcheck disable=SC2329  # invocada pelo `trap`, que o shellcheck nao segue
# cleanup roda em qualquer saida -- inclusive falha no meio. Droplet de teste
# esquecido cobra todo mes e ninguem associa a este script.
cleanup() {
  local id
  id="$(timeout 30s doctl compute droplet list --format Name,ID --no-header 2>/dev/null \
        | awk -v n="$probeName" '$1==n {print $2}' | head -1)"
  if [ -n "$id" ]; then
    echo
    echo "limpando: destruindo o droplet de teste $id"
    timeout 60s doctl compute droplet delete "$id" --force >/dev/null 2>&1 \
      && echo "  ✅ destruido" || echo "  ⚠️  NAO destruido -- apagar na mao: doctl compute droplet delete $id --force"
  fi
}
trap cleanup EXIT INT TERM

echo "=== 1. gerando o user-data ==="
userData="$(mktemp "${TMPDIR:-/tmp}/cloudinit-probe.XXXXXX.yaml")"
trap 'rm -f "$userData"; cleanup' EXIT INT TERM
./scripts/29-nixos-cloudinit.sh > "$userData" 2>/dev/null
size="$(wc -c < "$userData" | tr -d ' ')"
if [ "${size:-0}" -eq 0 ]; then
  fail "o gerador nao produziu nada"
  exit 1
fi
ok "user-data com ${size} bytes"

echo
echo "=== 2. criando o droplet de teste (nao toca no agent-computer) ==="
sshKeyID="$(timeout 30s doctl compute ssh-key list --format ID --no-header 2>/dev/null | head -1)"
created="$(timeout 120s doctl compute droplet create "$probeName" \
  --image ubuntu-24-04-x64 --size "$probeSize" --region nyc3 \
  --ssh-keys "$sshKeyID" --user-data-file "$userData" \
  --format ID --no-header --wait 2>&1)"
probeID="$(printf '%s' "$created" | tr -d '[:space:]')"
case "$probeID" in
  ''|*[!0-9]*) fail "criacao recusada: $created"; exit 1 ;;
esac
ok "droplet $probeID criado"

probeIP="$(timeout 30s doctl compute droplet get "$probeID" \
  --format PublicIPv4 --no-header 2>/dev/null | tr -d '[:space:]')"
ok "IP $probeIP"

echo
echo "=== 3. esperando o cloud-init terminar ==="
# Espera o ARQUIVO, nao o ciclo do cloud-init.
#
# `cloud-init status --wait` aguarda tudo, e o `runcmd` deste user-data dispara
# o `nixos-infect` -- 10 a 20 minutos. `write_files` roda ANTES do runcmd, entao
# esperar o ciclo inteiro e esperar por outra coisa: media a instalacao do NixOS
# em vez do transporte do user-data, que e o que este teste existe para provar.
#
# `test -s` e nao `test -f`: arquivo criado e ainda vazio existe, e o teste
# seguiria para o sha256 comparando contra nada.
ready=0
for _ in $(seq 1 30); do
  if timeout 15s ssh -i "$SSH_KEY_FILE" -o StrictHostKeyChecking=accept-new \
       -o ConnectTimeout=8 "root@${probeIP}" \
       'test -s /etc/nixos/host.nix' 2>/dev/null; then
    ready=1
    break
  fi
  sleep 20
done
if [ "$ready" = "1" ]; then
  ok "cloud-init concluiu e o arquivo existe"
else
  fail "o arquivo nunca apareceu"
  exit 1
fi

echo
echo "=== 4. o arquivo chegou IDENTICO? (a pergunta que motiva o script) ==="
# sha256 dos dois lados. Comparar tamanho nao basta: descompressao truncada
# produz arquivo menor, mas um erro de codificacao pode manter o tamanho.
localSum="$(python3 -c "
import hashlib, pathlib
print(hashlib.sha256(pathlib.Path('nixos/host.nix').read_bytes().rstrip(b'\n')).hexdigest())
")"
remoteSum="$(timeout 30s ssh -i "$SSH_KEY_FILE" -o StrictHostKeyChecking=accept-new \
  "root@${probeIP}" "python3 -c \"
import hashlib
print(hashlib.sha256(open('/etc/nixos/host.nix','rb').read().rstrip(b'\n')).hexdigest())
\"" 2>/dev/null | tr -d '[:space:]')"

echo "  local : ${localSum:0:32}"
echo "  remoto: ${remoteSum:0:32}"
if [ -n "$remoteSum" ] && [ "$localSum" = "$remoteSum" ]; then
  ok "o host.nix chegou byte a byte igual -- o cloud-init descomprimiu certo"
else
  fail "o conteudo DIFERE: o gz+b64 nao sobreviveu ao caminho ate o disco"
fi

echo
echo "=== 5. os auxiliares tambem ==="
for name in screen-add screen-remove session-sync agent-status; do
  remote="$(timeout 20s ssh -i "$SSH_KEY_FILE" -o StrictHostKeyChecking=accept-new \
    "root@${probeIP}" "wc -c < /etc/nixos/scripts/${name}.sh 2>/dev/null" 2>/dev/null | tr -d '[:space:]')"
  expected="$(python3 -c "
import pathlib
print(len(pathlib.Path('nixos/scripts/${name}.sh').read_bytes().rstrip(b'\n')))
")"
  # O bloco literal do YAML remove a quebra final, entao a diferenca de 1 byte
  # e esperada e nao e defeito.
  diff=$(( ${remote:-0} - expected ))
  if [ "${remote:-0}" -gt 0 ] && [ "$diff" -ge 0 ] && [ "$diff" -le 1 ]; then
    ok "${name}.sh: ${remote} bytes"
  else
    fail "${name}.sh: ${remote:-ausente} bytes, esperado ~${expected}"
  fi
done

echo
echo "erros: $errors"
exit $((errors > 0))
