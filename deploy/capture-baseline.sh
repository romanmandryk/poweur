#!/usr/bin/env bash
# Snapshot the running pre-E13 deployment before its first upgrade (no secrets).
set -euo pipefail
root=/opt/apps/poweur
infra=/opt/infra
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) root=${2:?Missing --root path}; shift 2 ;;
    --infra) infra=${2:?Missing --infra path}; shift 2 ;;
    *) echo 'Usage: capture-baseline.sh [--root PATH] [--infra PATH]' >&2; exit 1 ;;
  esac
done
root=$(cd "$root" && pwd -P)
[[ ! -e "$root/.current" && ! -L "$root/.current" ]] || { echo 'A release is already recorded' >&2; exit 1; }
image=$(docker inspect --format '{{.Image}}' poweur-relay)
web=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/web"}}{{.Source}}{{end}}{{end}}' poweur-relay)
[[ $image =~ ^sha256:[0-9a-f]{64}$ && -n "$web" && -d "$web" ]] || { echo 'Expected a running relay image and /web bind mount' >&2; exit 1; }
mkdir -p "$root/.releases"
release=$(mktemp -d "$root/.releases/baseline-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")
complete=false
trap 'if [[ $complete == false ]]; then rm -rf "$release"; fi' EXIT
mkdir -p "$release/deploy/infra" "$release/apps/web"
tar -C "$infra" --exclude='.env' --exclude='.env.*' --exclude='.git' --exclude='*.pem' --exclude='*.key' -cf - . | tar -C "$release/deploy/infra" -xf -
cp -R "$web/." "$release/apps/web/"
# Pin only the relay service. Keep the existing volume names/configuration.
awk '
  /^  [^ ]/ { relay = ($0 == "  relay:") }
  relay && /^    image:/ { $0 = "    image: ${RELAY_IMAGE:?}"; pinned++ }
  { gsub(/\$\{RELAY_ENV_FILE:-apps\/api\/\.env.prod\}/, "${RELAY_ENV_FILE}"); gsub(/apps\/api\/\.env.prod/, "${RELAY_ENV_FILE}"); print }
  END { if (pinned != 1) exit 1 }
' "$root/docker-compose.prod.yml" > "$release/docker-compose.prod.yml"
printf '%s\n' "$image" > "$release/.relay-image"
ln -s "$release" "$root/.current"
complete=true
echo "Captured $release; keep its image and volumes until the upgrade is verified."
