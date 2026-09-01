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
# Gerando na hora, `scripts/30-nixos-validate.sh` verifica exatamente o que vai
# subir.
#
# # O que o instalador faz com isto
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
import base64
import gzip
import pathlib
import sys
import textwrap

repo = pathlib.Path(sys.argv[1])
channel = sys.argv[2]

# 🛑 O TETO DE 64 KiB DO PROVEDOR -- por que este arquivo comprime.
#
# O DigitalOcean recusa user-data acima de 65.536 bytes, e o `host.nix` sozinho
# tem ~47 KB. Em 31/08/2026 um bloco de comentario de 1.234 bytes levou o total
# a 65.657 e a criacao foi recusada -- DEPOIS de o `10-update.sh` ja ter
# destruido o droplet antigo. A maquina ficou inexistente por causa de um
# comentario.
#
# Encurtar o comentario devolveu 494 bytes de folga, o que so adia o problema:
# com a margem nessa ordem, o proximo paragrafo repete o incidente.
#
# `encoding: gz+b64` e do proprio cloud-init: ele descomprime ao escrever o
# arquivo. Duas propriedades que importam aqui:
#
#   1. base64 e ASCII PURO, entao o invariante que ja custou tres droplets
#      (byte acima de 127 duplo-codificado -> cloud-init recusa em silencio)
#      continua valendo -- e fica ate mais forte, porque nem os fontes precisam
#      mais ser ASCII para o transporte funcionar.
#   2. `mtime=0` no gzip torna a saida DETERMINISTICA. Sem isso o carimbo de
#      tempo entra no cabecalho, o user-data muda a cada execucao, e o gate de
#      tamanho mediria um numero diferente do que seria enviado.
COMPRESSION_THRESHOLD = 2048


def literalBlock(text, spaces):
    """Indenta texto para um bloco literal de YAML."""
    prefix = " " * spaces
    out = []
    for line in text.split("\n"):
        out.append(prefix + line if line else "")
    return "\n".join(out).rstrip("\n")


def embedded(path, spaces):
    """Devolve os campos YAML de um arquivo, comprimindo quando compensa.

    Arquivo pequeno sai como texto legivel: gzip tem ~20 bytes de cabecalho e o
    base64 infla 33%, entao comprimir um script de 800 bytes AUMENTA o
    user-data. Alem disso, texto legivel no YAML e o que permite conferir o que
    vai subir sem decodificar nada -- vantagem real que so vale a pena perder
    quando o arquivo e grande.
    """
    raw = path.read_bytes()
    if len(raw) < COMPRESSION_THRESHOLD:
        return "    content: |\n" + literalBlock(raw.decode("ascii"), spaces)

    packed = gzip.compress(raw, compresslevel=9, mtime=0)
    encoded = base64.b64encode(packed).decode("ascii")
    # Quebrado em 76 colunas por legibilidade; o decodificador de base64 do
    # cloud-init ignora as quebras de linha.
    wrapped = "\n".join(textwrap.wrap(encoded, 76))
    return "    encoding: gz+b64\n    content: |\n" + literalBlock(wrapped, spaces)


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
{embedded(repo / 'nixos' / 'host.nix', 6)}""",
    f"""  - path: /etc/nixos/agent-authorized-keys
    permissions: '0644'
{embedded(repo / 'nixos' / 'agent-authorized-keys', 6)}""",
]
for name in helpers:
    blocks.append(
        f"""  - path: /etc/nixos/scripts/{name}.sh
    permissions: '0644'
{embedded(repo / 'nixos' / 'scripts' / f'{name}.sh', 6)}"""
    )

header = """#cloud-config
# agent-computer -- caminho NixOS.
#
# ATENCAO: ASCII ESTRITO. O DigitalOcean corrompe user_data com qualquer byte
# acima de 127 (dupla codificacao UTF-8 -> caractere de controle C1), e o
# cloud-init recusa o arquivo INTEIRO em silencio: reporta "status: done", nao
# instala nada, e o droplet sobe vazio. Ja custou tres droplets.
#
# Este arquivo e GERADO por scripts/29-nixos-cloudinit.sh a partir de
# nixos/. Nao edite a saida -- edite a origem.

write_files:
"""

runcmd = f"""
runcmd:
  # Instala NixOS por cima do Ubuntu recem-criado, importando o nosso modulo.
  #
  # O script (nixos-infect) poe o Nix na maquina que ja esta rodando, constroi
  # o sistema a partir do /etc/nixos/host.nix e reinicia nele. Leva 10-20 min.
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
