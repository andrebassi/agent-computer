#!/bin/bash
# Detecta computador inalcançável e diz qual recuperação usar.
#
# A documentação manda usar "Recover" a partir do estado de erro. Sem detecção,
# não há estado de erro nenhum: alguém precisa perceber sozinho que a máquina
# não responde, e adivinhar entre reiniciar, reconstruir e restaurar.
#
# Os três diagnósticos são deliberadamente separados, porque levam a ações
# diferentes e é fácil confundi-los:
#
#   droplet não existe          -> task up          (criar)
#   droplet existe, sem rede    -> reiniciar        (menos destrutivo primeiro)
#   rede ok, serviços caídos    -> task restart     (nem toca no droplet)
#   nada disso resolve          -> task update      (reconstrói, preserva volume)
#
# A ordem importa: a documentação pede a recuperação MENOS destrutiva primeiro,
# e reconstruir uma máquina cujo problema era um serviço parado joga fora
# /scratch e os pacotes instalados sem necessidade.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

echo "diagnóstico do agent computer"
echo

# --- 1. o droplet existe? --------------------------------------------------
id="$(droplet_id)"
if [ -z "$id" ]; then
  echo "  🛑 o droplet NÃO EXISTE"
  vol="$(timeout 30s doctl compute volume list --format Name --no-header 2>/dev/null | grep -c "$VOLUME_NAME")"
  if [ "${vol:-0}" -gt 0 ]; then
    echo "     O volume durável está intacto — o trabalho não se perdeu."
    echo
    echo "  → task up"
  else
    echo "     ⚠️  O volume também não existe. Isto não é um computador"
    echo "        inalcançável, é um ambiente que nunca foi criado ou foi apagado."
    echo
    echo "  → task up   (cria volume e droplet do zero)"
  fi
  exit 2
fi

ip="$(droplet_ip)"
echo "  ✅ droplet $id existe, IP $ip"

# --- 2. o provedor considera o droplet ativo? ------------------------------
apiStatus="$(timeout 30s doctl compute droplet get "$id" --format Status --no-header 2>/dev/null | tr -d ' ')"
if [ "$apiStatus" != "active" ]; then
  echo "  🛑 o provedor reporta o droplet como '$apiStatus'"
  echo
  echo "  → doctl compute droplet-action power-on $id --wait"
  exit 2
fi
echo "  ✅ o provedor reporta 'active'"

# --- 3. a rede responde? ---------------------------------------------------
# Ping antes de SSH: separa "máquina fora do ar" de "SSH travado", que pedem
# ações diferentes.
if timeout 20s ping -c 3 -W 3 "$ip" >/dev/null 2>&1; then
  echo "  ✅ responde a ping"
  networkOk=1
else
  echo "  🛑 NÃO responde a ping"
  networkOk=0
fi

# --- 4. o SSH autentica? ---------------------------------------------------
if agent_ssh true 2>/dev/null; then
  echo "  ✅ SSH autentica"
  ssh_ok=1
else
  echo "  🛑 SSH não autentica"
  ssh_ok=0
fi

if [ "$ssh_ok" -eq 0 ]; then
  echo
  echo "  COMPUTADOR INALCANÇÁVEL."
  if [ "$networkOk" -eq 1 ]; then
    echo "  A máquina responde na rede mas o SSH não sobe — costuma ser disco"
    echo "  cheio, boot travado no emergency shell, ou o serviço parado."
    echo
    echo "  Na ordem, do menos ao mais destrutivo:"
    echo "    1. doctl compute droplet-action reboot $id --wait"
    echo "    2. task update      (reconstrói; PRESERVA /workspace)"
  else
    echo "  A máquina não responde nem a ping."
    echo
    echo "    1. doctl compute droplet-action power-cycle $id --wait"
    echo "    2. task update      (reconstrói; PRESERVA /workspace)"
  fi
  echo
  echo "  O volume durável não é tocado por nenhuma das duas."
  exit 2
fi

# --- 5. o estado durável está montado? -------------------------------------
if agent_ssh 'mountpoint -q /workspace'; then
  echo "  ✅ /workspace montado do volume"
else
  echo "  🛑 /workspace NÃO é o volume — o estado se perderia num update"
  echo
  echo "  → agent_ssh 'sudo mount -a' e conferir /etc/fstab"
  exit 2
fi

# --- 6. as telas estão de pé? ----------------------------------------------
downUnits=""
for unit in xvfb openbox x11vnc novnc chrome; do
  [ "$(agent_ssh "systemctl is-active ${unit}@1.service 2>/dev/null")" = "active" ] || downUnits="$downUnits ${unit}@1"
done
if [ -n "$downUnits" ]; then
  echo "  🛑 serviço(s) caído(s):$downUnits"
  echo
  echo "  A máquina está no ar — reconstruir seria exagero e jogaria fora"
  echo "  /scratch e pacotes instalados à toa."
  echo
  echo "  → task restart"
  exit 1
fi
echo "  ✅ as cinco units da tela 1 estão ativas"

# --- 7. há tarefa travando alguma tela? ------------------------------------
# Uma tarefa bloqueada é estado legítimo, não falha — mas ela impede tarefas
# novas e sobrevive a reboot, então quem diagnostica precisa vê-la.
activeTasks="$(agent_ssh "python3 -c \"
import json,glob
for f in glob.glob('/workspace/agent/tasks/*.json'):
    t=json.load(open(f))
    if t['State'] in ('pending','running','blocked'):
        print(f\\\"{t['ID']} tela {t['Screen']} {t['State']}\\\")
\" 2>/dev/null")"
if [ -n "$activeTasks" ]; then
  echo
  echo "  ℹ️  tarefas ocupando tela (não é falha, mas impede tarefas novas):"
  echo "$activeTasks" | sed 's/^/       /'
  echo "     → agentd -resume -task <id>   ou   agentd -abandon -task <id>"
fi

echo
echo "  computador saudável"
exit 0
