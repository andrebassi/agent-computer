#!/bin/bash
# Validação ponta a ponta: da unit systemd até o pixel na tela.
#
# Não aborta na primeira falha — soma os erros e devolve o total no rc, para
# uma execução mostrar TODOS os problemas em vez de um por vez. Um script de
# validação que nunca falha não está validando, está decorando: cada seção
# aqui existe porque tem como dar errado de verdade.
source "$(dirname "$0")/lib.sh"
set -uo pipefail
load_token

errs=0
fail() { echo "  🛑 $1"; errs=$((errs+1)); }
ok()   { echo "  ✅ $1"; }

ip="$(droplet_ip)"
[ -z "$ip" ] && { echo "🛑 droplet nao existe"; exit 1; }
echo "validando agent computer em $ip"

echo
echo "=== 1. cloud-init aceitou o user-data ==="
if agent_ssh 'sudo cloud-init status --long 2>&1' | grep -qi "Failed loading yaml"; then
  fail "cloud-init RECUSOU o YAML — nada foi instalado"
else
  ok "user-data aceito"
fi
if [ "$(agent_ssh 'cat /var/lib/agent-computer-ready 2>/dev/null')" = "READY" ]; then
  ok "marca de conclusao presente"
else
  fail "marca /var/lib/agent-computer-ready ausente"
fi

echo
echo "=== 2. units systemd da tela 1 ==="
for unit in xvfb openbox x11vnc novnc chrome; do
  st="$(agent_ssh "systemctl is-active ${unit}@1.service 2>/dev/null")"
  [ "$st" = "active" ] && ok "${unit}@1: $st" || fail "${unit}@1: ${st:-sem resposta}"
done

echo
echo "=== 2b. estado durável em volume separado ==="
# A checagem que mais importa. Sem ela, tudo funciona e o trabalho some no
# primeiro update — e o sintoma não existe até ser tarde demais.
if agent_ssh 'mountpoint -q /workspace'; then
  ok "/workspace é ponto de montagem (volume separado)"
  dev="$(agent_ssh 'findmnt -no SOURCE /workspace 2>/dev/null')"
  case "$dev" in
    /dev/sd*|/dev/disk/by-id/scsi-0DO_Volume*) ok "montado de $dev" ;;
    *) fail "montado de origem inesperada: $dev" ;;
  esac
else
  fail "/workspace está no DISCO DO DROPLET — some num update"
fi
if [ "$(agent_ssh 'cat /var/lib/agent-computer-volume 2>/dev/null')" = "VOLUME_MONTADO" ]; then
  ok "cloud-init confirmou a montagem"
else
  fail "cloud-init não montou o volume"
fi
# nofail no fstab: sem ele, um boot sem o volume cai em emergency shell e não
# há nem SSH para diagnosticar.
if agent_ssh 'grep -q "/workspace.*nofail" /etc/fstab'; then
  ok "fstab tem nofail (boot não trava se o volume faltar)"
else
  fail "fstab sem nofail — boot sem volume cai em emergency shell"
fi

echo
echo "=== 2c. fronteira durável x descartável ==="
agent_ssh 'test -d /scratch' && ok "/scratch existe (efêmero declarado)" || fail "/scratch ausente"
agent_ssh 'test -d /workspace/browser' && ok "/workspace/browser (perfis)" || fail "/workspace/browser ausente"

echo
echo "=== 3. portas ouvindo (todas devem ser 127.0.0.1) ==="
ports="$(agent_ssh 'ss -lnt 2>/dev/null')"
for p in 5901 6081 9221; do
  line="$(echo "$ports" | grep ":$p " | head -1)"
  if [ -z "$line" ]; then
    fail "porta $p nao esta ouvindo"
  elif echo "$line" | grep -qE '127\.0\.0\.1:'"$p"; then
    ok "porta $p ouvindo apenas em loopback"
  else
    fail "porta $p EXPOSTA fora de loopback: $line"
  fi
done

echo
echo "=== 4. firewall ==="
ufw="$(agent_ssh 'sudo ufw status 2>/dev/null')"
echo "$ufw" | grep -q "Status: active" && ok "ufw ativo" || fail "ufw inativo"
for p in 5901 6081 9221; do
  echo "$ufw" | grep -q "^$p" && fail "ufw permite $p de fora" || ok "ufw nao expoe $p"
done

echo
echo "=== 5. tela X responde ==="
res="$(agent_ssh 'DISPLAY=:1 xdpyinfo 2>/dev/null | grep dimensions')"
if echo "$res" | grep -q "1920x1080"; then ok "X em 1920x1080"; else fail "X nao responde ou resolucao inesperada: ${res:-vazio}"; fi

echo
echo "=== 6. Chrome vivo e com o perfil certo ==="
if agent_ssh 'pgrep -f google-chrome-stable >/dev/null' ; then ok "processo Chrome no ar"; else fail "Chrome nao esta rodando"; fi
ver="$(agent_ssh 'curl -s --max-time 5 http://127.0.0.1:9221/json/version 2>/dev/null')"
if echo "$ver" | grep -q "Chrome/"; then
  ok "CDP responde: $(echo "$ver" | sed -n 's/.*"Browser": *"\([^"]*\)".*/\1/p')"
else
  fail "CDP em 9221 nao responde"
fi
if agent_ssh 'test -d /workspace/browser/screen-1/Default'; then ok "perfil da tela 1 em /workspace/browser/screen-1"; else fail "perfil do Chrome nao esta em /workspace"; fi

echo
echo "=== 7. noVNC serve a pagina ==="
code="$(agent_ssh 'curl -s -o /dev/null -w "%{http_code}" --max-time 5 http://127.0.0.1:6081/vnc.html 2>/dev/null')"
[ "$code" = "200" ] && ok "noVNC responde HTTP 200" || fail "noVNC devolveu HTTP ${code:-sem resposta}"

echo
echo "=== 8. captura de tela real (a prova final: tem pixel?) ==="
# Um X pode responder a xdpyinfo com a tela preta. Capturar e medir o tamanho
# do PNG e a unica prova de que ha algo desenhado.
shot="$(agent_ssh 'DISPLAY=:1 scrot -o /tmp/check.png 2>/dev/null && stat -c %s /tmp/check.png 2>/dev/null')"
if [ -n "$shot" ] && [ "$shot" -gt 20000 ] 2>/dev/null; then
  ok "captura com ${shot} bytes — ha conteudo desenhado"
else
  fail "captura vazia ou minuscula (${shot:-falhou}) — tela provavelmente preta"
fi

echo
echo "=== 9. recursos ==="
agent_ssh 'free -m | awk "/^Mem:/ {printf \"  RAM: %s MB usados de %s MB (%.0f%%)\n\", \$3, \$2, \$3/\$2*100}"; df -h / | awk "NR==2 {printf \"  disco: %s de %s (%s)\n\", \$3, \$2, \$5}"'

echo
echo "=== 10. agente de código, para a delegação ==="
# Sem isto, a falha do `npm install -g` no cloud-init só apareceria na primeira
# tarefa que delegasse — como "o agente de código não está configurado", que
# manda procurar no arquivo de credencial em vez de na instalação.
marker="$(agent_ssh 'cat /var/lib/agent-computer-code-agent 2>/dev/null')"
agentVersion="$(agent_ssh 'claude --version 2>/dev/null' | head -1)"
if [ -n "$agentVersion" ]; then
  ok "agente de código instalado: $agentVersion"
elif [ "$marker" = "FALHOU" ]; then
  fail "o cloud-init NÃO conseguiu instalar o agente de código — delegação indisponível"
else
  fail "agente de código ausente e sem marcador — cloud-init anterior à correção?"
fi

# A credencial é estado durável e mora no volume: ela sobrevive ao rebuild que
# reinstala o binário. Faltar aqui é diagnóstico diferente de faltar o binário,
# e a correção é outra — por isso são duas checagens, e não uma.
if agent_ssh 'test -f /workspace/agent/anthropic.env' 2>/dev/null; then
  perm="$(agent_ssh 'stat -c %a /workspace/agent/anthropic.env 2>/dev/null')"
  if [ "$perm" = "600" ]; then
    ok "credencial do agente de código presente, permissão $perm"
  else
    fail "credencial com permissão $perm — deve ser 600"
  fi
else
  fail "credencial ausente em /workspace/agent/anthropic.env — a delegação vai falhar"
fi

echo
echo "erros: $errs"
exit $errs
