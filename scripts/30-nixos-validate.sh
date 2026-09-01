#!/bin/bash
# Avalia a configuracao NixOS INTEIRA no Mac, antes de gastar um droplet.
#
# # Por que existe
#
# O dono decidiu reconstruir direto, sem droplet paralelo de validacao. Isso
# torna cara toda falha que so aparece no boot -- e a maioria delas e barata de
# pegar aqui: opcao inexistente, tipo errado, atributo duplicado, pacote com
# nome errado, asserção do proprio NixOS.
#
# Ja pegou duas nesta sessao, e as duas teriam custado uma reconstrucao:
#
#   1. `websockify` NAO existe no topo do nixpkgs (e `python3Packages.websockify`).
#      Uma tela sem noVNC, descoberta so ao abrir o navegador.
#   2. Com `users.mutableUsers = false` e sem chave de root declarada, o NixOS
#      afirma: "Neither the root account nor any wheel user has a password or
#      SSH authorized key. You must set one to prevent being locked out."
#      Seria um droplet inalcancavel.
#
# # O que ele NAO faz
#
# Nao constroi nada. Avalia ate o `drvPath` do sistema, o que forca o modulo
# inteiro a ser resolvido sem baixar uma derivacao sequer. Build de verdade
# acontece na maquina, no primeiro boot.
#
# Nao substitui as tres suites: elas verificam COMPORTAMENTO na maquina, e isto
# verifica que a configuracao e valida. Sao coisas diferentes.
set -uo pipefail

repoRoot="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repoRoot"

nixpkgsChannel="nixos-25.11"
errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

echo "1/4 os arquivos que o modulo exige existem"
for f in nixos/host.nix nixos/agent-authorized-keys \
         nixos/scripts/screen-add.sh nixos/scripts/screen-remove.sh \
         nixos/scripts/session-sync.sh nixos/scripts/agent-status.sh; do
  [ -s "$f" ] && ok "$f" || fail "$f ausente ou vazio"
done
# A chave publica e lida em tempo de avaliacao; sem ela o modulo nem carrega, e
# o erro aponta para o Nix em vez de apontar para o arquivo que falta.
if [ -s nixos/agent-authorized-keys ] && ! grep -qE '^(ssh-rsa|ssh-ed25519|ecdsa-)' nixos/agent-authorized-keys; then
  fail "nixos/agent-authorized-keys nao parece uma chave publica"
fi

# ASCII ESTRITO, e nao apenas "sem caractere de controle".
#
# Tudo isto viaja no user-data, e o DigitalOcean corrompe user-data com QUALQUER
# byte nao-ASCII: ele duplo-codifica UTF-8, o C2 80 resultante e um caractere de
# controle C1, e o cloud-init RECUSA o arquivo inteiro em silencio -- reporta
# "status: done", nao instala nada, e o droplet sobe vazio.
#
# Ja custou tres droplets. Um `assercao` com cedilha num comentario bastaria.
echo
echo "1b/4 ASCII estrito (o user-data nao tolera um byte acima de 127)"
for f in nixos/host.nix nixos/scripts/*.sh nixos/agent-authorized-keys; do
  if python3 -c "
import sys
data = open('$f','rb').read()
try:
    data.decode('ascii')
except UnicodeDecodeError as e:
    ctx = data[max(0,e.start-45):e.start+10].decode('utf-8','replace')
    print('byte %s em %d, perto de %r' % (hex(data[e.start]), e.start, ctx[-40:]))
    sys.exit(1)
" 2>/dev/null; then
    ok "$(basename "$f")"
  else
    fail "$(basename "$f"): $(python3 -c "
data = open('$f','rb').read()
try:
    data.decode('ascii')
except UnicodeDecodeError as e:
    ctx = data[max(0,e.start-45):e.start+10].decode('utf-8','replace')
    print('byte %s perto de %r' % (hex(data[e.start]), ctx[-40:]))
")"
  fi
done

echo

# 🛑 TAMANHO DO USER-DATA -- o gate que faltava, e que custou um droplet.
#
# O DigitalOcean recusa user-data acima de 64 KiB, e o cloud-init do NixOS embute
# o `host.nix` inteiro. Em 31/08/2026 um bloco de comentario de 1.234 bytes levou
# o total a 65.657 -- 121 acima do teto.
#
# O que torna isto grave nao e o estouro: e QUANDO ele aparece. O `10-update.sh`
# ja havia parado os servicos, desmontado o volume e DESTRUIDO o droplet antigo
# quando a API recusou a criacao do novo. A maquina ficou inexistente por causa de
# um comentario, e nenhuma checagem local reprovava antes de chegar la.
#
# O aviso de folga curta e de proposito: quem o vir deve ENCURTAR COMENTARIO, nao
# aumentar o teto -- o teto e do provedor.
userDataLimit=65536
echo "1c/4 tamanho do user-data (o provedor recusa acima de ${userDataLimit} bytes)"
userDataBytes="$(bash scripts/29-nixos-cloudinit.sh 2>/dev/null | wc -c | tr -d ' ')"
if [ "${userDataBytes:-0}" -eq 0 ]; then
  fail "nao consegui gerar o cloud-init para medir"
elif [ "$userDataBytes" -gt "$userDataLimit" ]; then
  fail "user-data com ${userDataBytes} bytes, $((userDataBytes - userDataLimit)) acima do teto -- a criacao do droplet VAI ser recusada"
else
  headroom=$((userDataLimit - userDataBytes))
  if [ "$headroom" -lt 1024 ]; then
    ok "user-data com ${userDataBytes} bytes -- atencao: so ${headroom} de folga, encurtar comentario antes de acrescentar"
  else
    ok "user-data com ${userDataBytes} bytes (${headroom} de folga)"
  fi
fi

echo
echo "2/4 sintaxe dos auxiliares em shell"
for f in nixos/scripts/*.sh; do
  bash -n "$f" 2>/dev/null && ok "$(basename "$f")" || fail "$(basename "$f"): erro de sintaxe"
done

echo
echo "3/4 sintaxe do modulo Nix"
if timeout 120s nix-instantiate --parse nixos/host.nix >/dev/null 2>/tmp/nixos-check-parse.log; then
  ok "host.nix analisa"
else
  fail "host.nix nao analisa:"
  sed 's/^/      /' /tmp/nixos-check-parse.log | head -12
fi

echo
echo "4/4 avaliacao do sistema completo (x86_64-linux)"
# O stub REPLICA o que o instalador gera, e nao apenas o minimo para avaliar.
#
# Custou um droplet descobrir a diferenca. O stub antigo tinha so boot e raiz, e
# aprovou uma configuracao que a maquina recusou:
#
#   error: The option `system.stateVersion` has conflicting definition values:
#   - In `/etc/nixos/host.nix`: "25.11"
#   - In `/etc/nixos/configuration.nix`: "23.11"
#
# O instalador fixa 23.11 na config dele. Avaliar o nosso modulo SOZINHO nunca veria
# o conflito -- ele so existe quando os dois se encontram. Um verificador que
# nao reproduz o vizinho aprova o que a maquina reprova, que e o pior tipo de
# verde.
#
# Os valores abaixo vem do proprio instalador (funcao makeConf do nixos-infect).
evalOut="$(timeout 900s nix --extra-experimental-features 'nix-command flakes' \
  eval --impure --raw --expr "
let
  nixpkgs = builtins.fetchTarball \"https://github.com/NixOS/nixpkgs/archive/${nixpkgsChannel}.tar.gz\";
  system = import \"\${nixpkgs}/nixos/lib/eval-config.nix\" {
    system = \"x86_64-linux\";
    modules = [ ./nixos/host.nix ({ modulesPath, ... }: {
      boot.loader.grub.device = \"/dev/vda\";
      fileSystems.\"/\" = { device = \"/dev/vda1\"; fsType = \"ext4\"; };
      # O que o nixos-infect escreve em configuration.nix:
      system.stateVersion = \"23.11\";
      services.openssh.enable = true;
      users.users.root.openssh.authorizedKeys.keys = [ \"ssh-rsa STUB\" ];
      networking.hostName = \"agent-computer\";
      boot.loader.grub.efiSupport = false;
    }) ];
  };
in \"drvPath: \${system.config.system.build.toplevel.drvPath}\"
" 2>&1)"
if echo "$evalOut" | grep -q '^drvPath: /nix/store/'; then
  ok "$(echo "$evalOut" | grep '^drvPath:')"
  # Aviso do NixOS nao reprova, mas precisa aparecer: o que ele costuma
  # denunciar e ordenacao de unidade sem dependencia, que falha em silencio.
  if echo "$evalOut" | grep -q 'evaluation warning'; then
    echo "$evalOut" | grep 'evaluation warning' | sed 's/^/  ⚠️  /'
  fi
else
  fail "a avaliacao falhou:"
  echo "$evalOut" | tail -20 | sed 's/^/      /'
fi

echo
echo "erros: $errs"
exit $errs
