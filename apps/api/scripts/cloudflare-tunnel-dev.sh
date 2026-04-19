#!/usr/bin/env bash
set -euo pipefail

TUNNEL_NAME="${TUNNEL_NAME:-localrelay}"
TUNNEL_HOSTNAME="${TUNNEL_HOSTNAME:-${RELAY_HOSTNAME:-localrelay.poweur.net}}"
TARGET_URL="${TARGET_URL:-http://localhost:8080}"

if ! command -v cloudflared >/dev/null 2>&1; then
  echo "cloudflared is required (brew install cloudflared)" >&2
  exit 1
fi

ORIGIN_CERT="${TUNNEL_ORIGIN_CERT:-}"
if [[ -z "$ORIGIN_CERT" ]]; then
  for candidate in "$HOME/.cloudflared/cert.pem" "$HOME/.cloudflare-warp/cert.pem" "$HOME/cloudflare-warp/cert.pem" "/etc/cloudflared/cert.pem" "/usr/local/etc/cloudflared/cert.pem"; do
    if [[ -f "$candidate" ]]; then
      ORIGIN_CERT="$candidate"
      break
    fi
  done
fi

if [[ -z "$ORIGIN_CERT" ]]; then
  echo "Cloudflare origin cert not found. Run 'cloudflared login' or set TUNNEL_ORIGIN_CERT." >&2
  exit 1
fi

echo "Ensuring tunnel name is free: ${TUNNEL_NAME}"
cloudflared tunnel delete --force "$TUNNEL_NAME" >/dev/null 2>&1 || true
cloudflared tunnel create "$TUNNEL_NAME"

echo "Using tunnel name: ${TUNNEL_NAME}"
echo "Using hostname: ${TUNNEL_HOSTNAME}"
echo "Using target: ${TARGET_URL}"

if cloudflared tunnel route dns --help | awk '/--overwrite-dns/ {found=1} END {exit !found}'; then
  if cloudflared tunnel route dns --overwrite-dns "$TUNNEL_NAME" "$TUNNEL_HOSTNAME"; then
    echo "DNS route ensured for ${TUNNEL_HOSTNAME}."
  else
    echo "Failed to ensure DNS route for ${TUNNEL_HOSTNAME}." >&2
    exit 1
  fi
else
  if cloudflared tunnel route dns "$TUNNEL_NAME" "$TUNNEL_HOSTNAME"; then
    echo "DNS route created for ${TUNNEL_HOSTNAME}."
  else
    echo "DNS route already exists for ${TUNNEL_HOSTNAME}; continuing."
  fi
fi

cloudflared tunnel run --url "$TARGET_URL" "$TUNNEL_NAME"
