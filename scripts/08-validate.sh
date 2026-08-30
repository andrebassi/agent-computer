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
echo "=== 2. units systemd ==="
for unit in xvfb openbox x11vnc novnc chrome; do
  st="$(agent_ssh "systemctl is-active ${unit}.service 2>/dev/null")"
  [ "$st" = "active" ] && ok "$unit: $st" || fail "$unit: ${st:-sem resposta}"
done

echo
echo "=== 3. portas ouvindo (todas devem ser 127.0.0.1) ==="
ports="$(agent_ssh 'ss -lnt 2>/dev/null')"
for p in 5900 6080 9222; do
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
for p in 5900 6080 9222; do
  echo "$ufw" | grep -q "^$p" && fail "ufw permite $p de fora" || ok "ufw nao expoe $p"
done

echo
echo "=== 5. tela X responde ==="
res="$(agent_ssh 'DISPLAY=:1 xdpyinfo 2>/dev/null | grep dimensions')"
if echo "$res" | grep -q "1920x1080"; then ok "X em 1920x1080"; else fail "X nao responde ou resolucao inesperada: ${res:-vazio}"; fi

echo
echo "=== 6. Chrome vivo e com o perfil certo ==="
if agent_ssh 'pgrep -f google-chrome-stable >/dev/null' ; then ok "processo Chrome no ar"; else fail "Chrome nao esta rodando"; fi
ver="$(agent_ssh 'curl -s --max-time 5 http://127.0.0.1:9222/json/version 2>/dev/null')"
if echo "$ver" | grep -q "Chrome/"; then
  ok "CDP responde: $(echo "$ver" | sed -n 's/.*"Browser": *"\([^"]*\)".*/\1/p')"
else
  fail "CDP em 9222 nao responde"
fi
if agent_ssh 'test -d /workspace/.browser/Default'; then ok "perfil persistente em /workspace/.browser"; else fail "perfil do Chrome nao esta em /workspace"; fi

echo
echo "=== 7. noVNC serve a pagina ==="
code="$(agent_ssh 'curl -s -o /dev/null -w "%{http_code}" --max-time 5 http://127.0.0.1:6080/vnc.html 2>/dev/null')"
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
echo "erros: $errs"
exit $errs
