#!/bin/bash
# Monta o cloud-init do caminho NixOS e escreve na saida padrao.
#
# # Por que GERADO, e nao um arquivo versionado
#
# O user-data precisa carregar o `nixos/host.nix` inteiro e os quatro
# auxiliares. Manter uma copia deles dentro de um YAML seria manter duas
# verdades: alguem corrige o modulo, esquece a copia, e o droplet novo sobe com
# a versao velha -- sem erro nenhum, porque as duas sao YAML valido.
#
# Gerando na hora, `scripts/30-nixos-check.sh` verifica exatamente o que vai
# subir.
#
# # O que o nixos-infect faz com isto
#
# Ele gera `configuration.nix` E `hardware-configuration.nix` E a rede do
# DigitalOcean, e importa o nosso modulo por NIXOS_IMPORT.
#
# 🛑 NAO trocar NIXOS_IMPORT por NIXOS_CONFIG, nem pre-escrever
# `/etc/nixos/configuration.nix`. O guard
# `[[ -e /etc/nixos/configuration.nix ]] && return 0` do script aborta a funcao
# INTEIRA -- o que pula tambem o hardware e a configuracao de REDE. A maquina
# subiria inalcancavel, e o unico caminho de volta seria recria-la.
set -euo pipefail

repoRoot="$(cd "$(dirname "$0")/.." && pwd)"

# O canal fica aqui e no verificador. Divergir os dois faria o Mac aprovar uma
# configuracao que a maquina constroi com outro nixpkgs.
NIX_CHANNEL="${NIX_CHANNEL:-nixos-25.11}"

python3 - "$repoRoot" "$NIX_CHANNEL" <<'PYRENDER'
import pathlib
import sys

repo = pathlib.Path(sys.argv[1])
channel = sys.argv[2]


def indented(path, spaces):
    """Devolve o conteudo do arquivo pronto para um bloco literal de YAML.

    Bloco literal (`|`) preserva o texto byte a byte, que e o que se quer para
    codigo. Linha vazia sai vazia mesmo, sem a indentacao -- YAML aceita, e
    acrescentar espacos ali produziria diferenca invisivel entre o arquivo do
    repositorio e o que chega na maquina.
    """
    prefix = " " * spaces
    out = []
    for line in path.read_text(encoding="ascii").split("\n"):
        out.append(prefix + line if line else "")
    return "\n".join(out).rstrip("\n")


helpers = ["screen-add", "screen-remove", "session-sync", "agent-status"]

blocks = [
    f"""  - path: /etc/nixos/host.nix
    permissions: '0644'
    content: |
{indented(repo / 'nixos' / 'host.nix', 6)}""",
    f"""  - path: /etc/nixos/agent-authorized-keys
    permissions: '0644'
    content: |
{indented(repo / 'nixos' / 'agent-authorized-keys', 6)}""",
]
for name in helpers:
    blocks.append(
        f"""  - path: /etc/nixos/scripts/{name}.sh
    permissions: '0644'
    content: |
{indented(repo / 'nixos' / 'scripts' / f'{name}.sh', 6)}"""
    )

header = """#cloud-config
# agent-computer -- caminho NixOS.
#
# ATENCAO: ASCII ESTRITO. O DigitalOcean corrompe user_data com qualquer byte
# acima de 127 (dupla codificacao UTF-8 -> caractere de controle C1), e o
# cloud-init recusa o arquivo INTEIRO em silencio: reporta "status: done", nao
# instala nada, e o droplet sobe vazio. Ja custou tres droplets.
#
# Este arquivo e GERADO por scripts/29-render-nixos-userdata.sh a partir de
# nixos/. Nao edite a saida -- edite a origem.

write_files:
"""

runcmd = f"""
runcmd:
  # Converte o Ubuntu recem-criado em NixOS, importando o nosso modulo.
  #
  # PROVIDER=digitalocean e o que faz o script gerar a configuracao de REDE da
  # plataforma; sem isso a maquina volta do reboot sem rota e inalcancavel.
  #
  # A saida vai para /tmp/infect.log porque o processo apaga o root filesystem
  # ao completar: quando algo falha no meio, esse log e a unica testemunha.
  - [ bash, -c, "curl -fsSL https://raw.githubusercontent.com/elitak/nixos-infect/master/nixos-infect | PROVIDER=digitalocean NIXOS_IMPORT=./host.nix NIX_CHANNEL={channel} bash 2>&1 | tee /tmp/infect.log" ]
"""

sys.stdout.write(header + "\n\n".join(blocks) + "\n" + runcmd)
PYRENDER
