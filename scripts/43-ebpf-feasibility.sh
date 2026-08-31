#!/bin/bash
# Mede se a maquina suporta o coletor eBPF, e decide o desenho da unidade.
#
# Existe porque a premissa "eBPF funciona aqui" era SUPOSICAO: ate 31/08/2026 o
# repositorio inteiro nao tinha um unico `uname -r` registrado, e um grep por
# `BTF|eBPF|bpf` devolvia zero. Plano construido sobre suposicao de kernel se
# descobre errado depois de escrever o programa em C, que e o momento mais caro.
#
# Ele NAO constroi nada. Mede, imprime, e reprova quando o minimo nao esta la --
# inclusive depois, quando um `task update` ou um `nixos-rebuild` trocar o kernel
# sem ninguem pedir.
#
# Tres saidas dele decidem o desenho, e por isso ele roda ANTES de qualquer codigo:
#
#   BTF presente?          -> fentry/LSM entram no plano, ou saem dele
#   quem le o tracefs?     -> unidade com usuario proprio, ou unidade root
#   offsets dos hooks      -> as structs em C sao escritas a partir do `format`
#
# Roda como os DOIS usuarios de proposito: a diferenca entre o que `agent` ve e o
# que `root` ve e justamente a informacao que decide a unidade.
source "$(dirname "$0")/lib.sh"
set +e
load_token

# Os hooks que o coletor pretende usar. A ordem e a das fatias do plano.
#
# Sobrescritivel por ambiente, e nao por conveniencia: e assim que a PROVA DE
# FALHA roda. Um gate que nunca reprovou provavelmente nao e gate, e a unica
# forma de provar que este enxerga e pedir um hook que nao existe:
#
#   EBPF_TRACEPOINTS="sched/hook_que_nao_existe" ./scripts/43-ebpf-feasibility.sh
#   -> tem que sair rc != 0
TRACEPOINTS="${EBPF_TRACEPOINTS:-sched/sched_process_exec sched/sched_process_fork sched/sched_process_exit sock/inet_sock_set_state syscalls/sys_enter_connect syscalls/sys_exit_connect signal/signal_generate oom/mark_victim syscalls/sys_enter_openat}"

# Contagem de reprovacoes. O script NAO aborta no primeiro erro: um gate que para
# na primeira falha esconde as outras, e o operador conserta uma por rodada.
failures=0

# fail registra uma reprovacao com a mensagem, sem interromper o restante.
fail() {
  echo "  🛑 $1"
  failures=$((failures + 1))
}

echo "=== 1. identidade da maquina ==="
# Sem isto, toda linha abaixo e anedota sobre uma maquina nao identificada.
identity="$(agent_ssh 'echo "$(uname -r)|$(uname -m)|$(. /etc/os-release; echo "$ID $VERSION_ID")"' 2>/dev/null | tr -d '\r')"
kernelRelease="$(echo "$identity" | cut -d'|' -f1)"
machineArch="$(echo "$identity" | cut -d'|' -f2)"
distroName="$(echo "$identity" | cut -d'|' -f3)"

if [ -z "$kernelRelease" ]; then
  fail "a maquina nao respondeu -- rode 'task health' antes deste gate"
  echo
  echo "erros: $failures"
  exit 1
fi
echo "  kernel:  $kernelRelease"
echo "  arch:    $machineArch"
echo "  distro:  $distroName"

# O coletor e compilado com GOARCH=amd64 e o objeto BPF com -target bpf. Uma
# maquina ARM exigiria outro alvo nos dois, e o erro apareceria so no deploy.
case "$machineArch" in
  x86_64) ;;
  *) fail "arquitetura $machineArch: o objeto BPF e o binario precisam de outro alvo" ;;
esac

echo
echo "=== 2. BTF ==="
# BTF decide se CO-RE, fentry e LSM entram no plano. As probes de tracepoint das
# fatias 1 a 5 NAO precisam dele -- e por isso a falta aqui degrada o plano em vez
# de mata-lo.
btfSize="$(root_ssh 'stat -c %s /sys/kernel/btf/vmlinux 2>/dev/null' 2>/dev/null | tr -d '\r')"
if [ -n "$btfSize" ] && [ "$btfSize" -gt 0 ] 2>/dev/null; then
  echo "  ✅ /sys/kernel/btf/vmlinux presente ($btfSize bytes) -- CO-RE viavel"
else
  echo "  ⚠️  BTF ausente -- fatia 6 (fentry/LSM) sai do plano; as fatias 1-5 seguem"
fi

echo
echo "=== 3. bpffs ==="
if root_ssh 'mount | grep -q " type bpf "' 2>/dev/null; then
  echo "  ✅ bpffs montado"
else
  fail "bpffs nao montado -- mapa fixado (pinning) nao funciona"
fi

echo
echo "=== 4. tracefs: QUEM le decide o desenho da unidade ==="
# Este e o item que decide entre usuario proprio e root, e ele e de DAC, nao de
# capacidade: o `cilium/ebpf` atacha tracepoint por perf_event_open, e o id do
# tracepoint vem de LER um arquivo. CAP_PERFMON nao abre arquivo sem permissao.
tracingMode="$(root_ssh 'stat -c "%a %U:%G" /sys/kernel/tracing 2>/dev/null' 2>/dev/null | tr -d '\r')"
echo "  /sys/kernel/tracing: ${tracingMode:-ilegivel}"

if agent_ssh 'cat /sys/kernel/tracing/events/sched/sched_process_exec/id' >/dev/null 2>&1; then
  echo "  ✅ um NAO-root le o id do tracepoint"
  echo "     -> unidade FORMA A: User=agentprobe + AmbientCapabilities=CAP_BPF CAP_PERFMON"
else
  echo "  ℹ️  so root le o id do tracepoint"
  echo "     -> unidade FORMA B: User=root + CapabilityBoundingSet apertado"
  echo "        (dar CAP_DAC_READ_SEARCH a um usuario 'nao-root' leria o cofre do"
  echo "         mesmo jeito -- seria encenacao de reducao, nao reducao)"
fi

echo
echo "=== 5. os hooks existem? ==="
for tracepoint in $TRACEPOINTS; do
  if root_ssh "test -d /sys/kernel/tracing/events/$tracepoint" 2>/dev/null; then
    echo "  ✅ $tracepoint"
  else
    fail "$tracepoint AUSENTE -- a probe que depende dele sai do plano"
  fi
done

echo
echo "=== 6. sysctl que afeta carregamento e atach ==="
for knob in kernel.unprivileged_bpf_disabled kernel.perf_event_paranoid kernel.pid_max; do
  value="$(root_ssh "sysctl -n $knob 2>/dev/null" 2>/dev/null | tr -d '\r')"
  echo "  $knob = ${value:-ilegivel}"
done
# pid_max entra na conta de reuso de PID: a correlacao com o span usa a chave
# (pid, start_ns) justamente porque pid sozinho se repete.

echo
echo "=== 7. orcamento: a linha-base contra a qual o coletor sera medido ==="
root_ssh 'free -m | awk "NR==2 {print \"  RAM: \" \$7 \" MB disponiveis de \" \$2}"; df -h /workspace | awk "NR==2 {print \"  volume: \" \$4 \" livres de \" \$2}"; echo "  vCPU: $(nproc)"' 2>/dev/null

echo
echo "=== 8. o que o coletor precisa e a maquina nao tem ==="
# Nao e reprovacao: o objeto BPF e compilado no Mac e vai commitado, justamente
# para que a maquina nunca precise de clang. bpftool so serve para diagnostico.
for binary in bpftool clang; do
  if root_ssh "command -v $binary >/dev/null" 2>/dev/null; then
    echo "  ✅ $binary presente"
  else
    echo "  ℹ️  $binary ausente (esperado; declarar em nixos/host.nix se for diagnosticar)"
  fi
done

echo
if [ "$failures" -eq 0 ]; then
  echo "✅ a maquina suporta o coletor eBPF  (erros: 0)"
else
  echo "🛑 o coletor NAO pode ser construido como planejado  (erros: $failures)"
fi
exit "$((failures > 0))"
