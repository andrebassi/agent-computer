#!/bin/bash
# Compila e instala o coletor eBPF na máquina, por SSH de root.
#
# Molde copiado de `16-deploy-agent.sh`, e a cópia é deliberada: as duas
# propriedades que aquele script garante valem igual aqui.
#
#   1. O GATE RODA ANTES. Instalar binário que não passou no gate é como não
#      ter gate.
#   2. O BINÁRIO É INSTALADO POR ROOT, em /usr/local/bin, root:root 0755.
#      É o achado 2 da revisão de segurança: "quem escreve o binário do serviço
#      é dono do serviço". Um coletor de auditoria que o auditado pudesse
#      reescrever seria pior que nenhum — ele daria a impressão de vigilância.
#
# ⚠️ O objeto BPF é compilado no MAC e vai commitado. A máquina não tem clang
# nem LLVM, e o usuário `agent` não pode instalá-los. Isso só é possível porque
# o programa usa tracepoint sem CO-RE: sem relocação dependente de BTF, o mesmo
# objeto carrega em qualquer kernel recente.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

probeDir="$(cd "$(dirname "$0")/../probe" && pwd)"
# Os dois programas de kernel. Compilar um e esquecer o outro produziria um
# binario que sobe, atacha metade, e nao registra a metade esquecida -- sem
# nenhuma mensagem, porque o objeto embutido antigo continua valido.
bpfPrograms="exec net"

echo "1/6 gate de cobertura"
if ! (cd "$probeDir" && ./scripts/coverage-gate.sh >/dev/null 2>&1); then
  echo "  🛑 o gate reprovou — rode ./probe/scripts/coverage-gate.sh para ver"
  exit 1
fi
echo "  ✅ cobertura aprovada"

echo
echo "2/6 compilando o objeto BPF"
# O clang da APPLE não serve: ele não traz o backend BPF e falha com
# "No available targets are compatible with triple bpf". Medido em 31/08/2026.
# O do nixpkgs registra bpf, bpfeb e bpfel.
# A busca COMPILA um objeto BPF de teste, e não consulta `-print-targets`.
#
# Medido em 31/08/2026, e é a razão de o teste ser este: o clang WRAPPER do
# nixpkgs passa no `-print-targets` (ele lista bpf) e mesmo assim falha na
# compilação, porque injeta `-mmacos-version-min` nos argumentos — flag que não
# faz sentido para o alvo bpf e que o `-Werror` transforma em erro. O wrapper
# avisa em voz alta: "cc-wrapper is currently not designed with multi-target
# compilers in mind".
#
# Perguntar "você sabe emitir BPF?" e perguntar "você CONSEGUE emitir BPF?" são
# perguntas diferentes, e só a segunda serve. Por isso os wrappers são pulados
# e a checagem é uma compilação de verdade.
#
# `find -L` (segue link) também não é detalhe: no nixpkgs `bin/clang` é um
# SYMLINK para `bin/clang-21`, e um `find -type f` sem `-L` não o enxerga —
# some justamente o compilador que serve, sobrando só os wrappers que não
# servem.
bpfClang=""
probeSource="$(mktemp -t bpfprobe).c"
printf 'int f(void){return 0;}\n' > "$probeSource"
for candidate in "${BPF_CLANG:-}" $(find -L /nix/store -maxdepth 3 -name 'clang*' -type f -perm -u+x 2>/dev/null | grep -v clang-wrapper | grep -E '/clang(-[0-9]+)?$' | sort -r) clang; do
  [ -n "$candidate" ] || continue
  [ -x "$candidate" ] || command -v "$candidate" >/dev/null 2>&1 || continue
  if "$candidate" -target bpf -O2 -Werror -c "$probeSource" -o /dev/null 2>/dev/null; then
    bpfClang="$candidate"
    break
  fi
done
rm -f "$probeSource"
if [ -z "$bpfClang" ]; then
  echo "  🛑 nenhum clang com backend BPF encontrado."
  echo "     O clang da Apple NÃO serve: ele falha com"
  echo "     \"No available targets are compatible with triple bpf\"."
  echo "     Entre no devShell (nix develop) ou aponte BPF_CLANG para um clang do nixpkgs."
  exit 1
fi
echo "  clang: $bpfClang"
for program in $bpfPrograms; do
  source_file="${probeDir}/internal/bpf/${program}.bpf.c"
  object_file="${probeDir}/cmd/agent-probe/${program}.bpf.o"
  if ! "$bpfClang" -target bpf -D__TARGET_ARCH_x86 -O2 -g -Wall -Werror \
       -c "$source_file" -o "$object_file"; then
    echo "  🛑 a compilação de ${program}.bpf.c falhou"
    exit 1
  fi
  echo "  ✅ ${program}.bpf.o ($(wc -c < "$object_file" | tr -d ' ') bytes)"
done

echo
echo "3/6 arquitetura da máquina"
# Perguntada à máquina, nunca presumida: um binário de arquitetura errada falha
# com "cannot execute binary file", que não aponta para a causa.
remoteArch="$(agent_ssh 'uname -m' 2>/dev/null | tr -d '\r')"
case "$remoteArch" in
  x86_64) goArch=amd64 ;;
  aarch64) goArch=arm64 ;;
  *) echo "  🛑 arquitetura desconhecida: '$remoteArch'"; exit 1 ;;
esac
echo "  $remoteArch -> GOARCH=$goArch"

echo
echo "4/6 compilando o coletor"
binPath="$(mktemp -t agent-probe)"
if ! (cd "$probeDir" && GOWORK=off GOOS=linux GOARCH="$goArch" CGO_ENABLED=0 \
      go build -trimpath -o "$binPath" ./cmd/agent-probe); then
  echo "  🛑 a compilação falhou"
  exit 1
fi
echo "  ✅ $(wc -c < "$binPath" | tr -d ' ') bytes"

echo
echo "5/6 instalando por SSH de root"
host="$(agent_host)"
if ! timeout 120s scp -i "$SSH_KEY_FILE" -o StrictHostKeyChecking=accept-new \
     "$binPath" "root@${host}:/root/.agent-probe-new" >/dev/null 2>&1; then
  echo "  🛑 a cópia falhou"
  rm -f "$binPath"
  exit 1
fi
rm -f "$binPath"
# `install` num passo: a troca é atômica, e o binário nunca existe com
# permissão intermediária.
root_ssh 'install -o root -g root -m 0755 /root/.agent-probe-new /usr/local/bin/agent-probe && rm -f /root/.agent-probe-new'
echo "  ✅ instalado"

echo
echo "6/6 provando a contenção pelo COMPORTAMENTO"
# Não basta ver o modo do arquivo: o que importa é o que o usuário do modelo
# CONSEGUE fazer. Testar a permissão pela tentativa é a diferença entre ler a
# configuração e verificar o efeito dela.
if agent_ssh 'test -w /usr/local/bin/agent-probe' 2>/dev/null; then
  echo "  🛑 o usuário 'agent' PODE escrever no binário do coletor."
  echo "     Quem escreve o binário do serviço é dono do serviço."
  exit 1
fi
echo "  ✅ o usuário 'agent' não escreve no binário do coletor"
root_ssh 'ls -l /usr/local/bin/agent-probe' 2>/dev/null | sed 's/^/  /'
