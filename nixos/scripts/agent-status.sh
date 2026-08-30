#!/bin/bash
# Estado das telas ativas, do estado duravel e dos recursos.
echo "=== telas ==="
for n in $(seq 1 9); do
  st="$(systemctl is-active xvfb@$n.service 2>/dev/null)"
  [ "$st" != "active" ] && continue
  printf "  tela %s: " "$n"
  for unit in openbox x11vnc novnc chrome; do
    printf "%s=%s " "$unit" "$(systemctl is-active ${unit}@$n.service)"
  done
  printf "| web 127.0.0.1:%s CDP 127.0.0.1:922%s\n" "$((6080 + n))" "$n"
done
echo
echo "=== estado duravel ==="
printf "  volume: %s\n" "$(cat /var/lib/agent-computer-volume 2>/dev/null || echo DESCONHECIDO)"
if mountpoint -q /workspace; then
  echo "  /workspace: volume separado (sobrevive a rebuild do droplet)"
else
  echo "  /workspace: NO DISCO DO DROPLET -- some se o droplet for recriado"
fi
df -h /workspace 2>/dev/null | tail -1 | sed "s/^/  /"
echo "  /scratch: descartavel, $(du -sh /scratch 2>/dev/null | cut -f1)"
echo
echo "=== portas ouvindo ==="
ss -lntp 2>/dev/null | grep -E ':(59[0-9][0-9]|60[0-9][0-9]|922[0-9])' || echo "  nenhuma"
echo
echo "=== recursos ==="
free -h | sed "s/^/  /"
df -h / | tail -1 | sed "s/^/  /"
