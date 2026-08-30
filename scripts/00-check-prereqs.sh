#!/bin/bash
# Confere tudo que o provisionamento vai precisar ANTES de gastar dinheiro:
# binários, token, chave SSH (pública E privada) e latência até a região.
source "$(dirname "$0")/lib.sh"
set +e   # herança de set -e mataria o script no primeiro check que falha
rc=0

echo "=== binarios ==="
for b in doctl ssh jq; do
  if command -v "$b" >/dev/null; then printf "  ✅ %s\n" "$b"
  else printf "  🛑 %s AUSENTE\n" "$b"; rc=1; fi
done

echo
echo "=== chave SSH ==="
if [ -f "$SSH_KEY_FILE" ]; then
  echo "  ✅ privada: $SSH_KEY_FILE"
else
  echo "  🛑 privada AUSENTE: $SSH_KEY_FILE"; rc=1
fi
if [ -f "${SSH_KEY_FILE}.pub" ]; then
  local_fp="$(ssh-keygen -E md5 -lf "${SSH_KEY_FILE}.pub" | awk '{print $2}' | sed 's/^MD5://')"
  echo "  ✅ publica, fingerprint: $local_fp"
else
  echo "  🛑 publica AUSENTE"; rc=1
fi

echo
echo "=== token DigitalOcean ==="
if load_token; then
  acct="$(timeout 30s doctl account get --format Email,Status --no-header 2>/dev/null)"
  if [ -n "$acct" ]; then echo "  ✅ conta: $acct"
  else echo "  🛑 token presente mas API recusou"; rc=1; fi
  # A chave da conta precisa bater com a local, senão o droplet nasce sem acesso.
  remote_fp="$(timeout 30s doctl compute ssh-key get "$SSH_KEY_ID" --format FingerPrint --no-header 2>/dev/null)"
  if [ "$remote_fp" = "$local_fp" ]; then echo "  ✅ chave da conta bate com a local"
  else echo "  🛑 chave da conta ($remote_fp) NAO bate com a local ($local_fp)"; rc=1; fi
else
  rc=1
fi

echo
echo "=== droplet ja existe? ==="
ip="$(droplet_ip)"
if [ -n "$ip" ]; then echo "  ℹ️  '$DROPLET_NAME' ja existe em $ip"
else echo "  ✅ nome '$DROPLET_NAME' livre"; fi

echo
echo "=== latencia ate $DROPLET_REGION (VNC interativo sofre acima de ~150ms) ==="
# Medição honesta: pinga um droplet REAL da mesma região. O antigo
# speedtest-<regiao>.digitalocean.com foi desativado e nem resolve em DNS —
# o curl falhava calado e o check reportava 0ms, que é pior que não medir.
probe_ip="$(timeout 30s doctl compute droplet list --format Name,PublicIPv4,Region --no-header 2>/dev/null \
  | awk -v r="$DROPLET_REGION" '$3==r {print $2; exit}')"
if [ -n "$probe_ip" ]; then
  avg="$(timeout 25s ping -c 5 "$probe_ip" 2>/dev/null | tail -1 | awk -F'/' '{print $5}')"
  if [ -n "$avg" ]; then
    ms="$(awk -v a="$avg" 'BEGIN{printf "%.0f", a}')"
    echo "  ${ms}ms medio, contra droplet real em $DROPLET_REGION ($probe_ip)"
    if [ "$ms" -gt 180 ] 2>/dev/null; then
      echo "  ⚠️  acima de 180ms — a tela vai arrastar; considerar outra regiao"
    elif [ "$ms" -gt 130 ] 2>/dev/null; then
      echo "  ℹ️  entre 130 e 180ms: usavel, com lag visivel no arrasto do mouse"
    fi
  else
    echo "  ⚠️  ping bloqueado — nao deu para medir"
  fi
else
  echo "  ℹ️  nenhum droplet em $DROPLET_REGION para servir de sonda ainda;"
  echo "     a medicao passa a valer depois do primeiro 'task up'"
fi

echo
echo "rc=$rc"
exit $rc
