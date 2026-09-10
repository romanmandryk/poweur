#!/usr/bin/env bash
# Executed from the tested commit by Actions. No remote git pull/latest race.
set -euo pipefail
root=${POWEUR_APP_DIR:-/opt/apps/poweur}
sha=${1:?Usage: release.sh COMMIT IMAGE}
image=${2:?Usage: release.sh COMMIT IMAGE}
[[ $sha =~ ^[0-9a-f]{40}$ ]] || { echo 'Expected a full git commit' >&2; exit 1; }
[[ $image =~ ^ghcr.io/[a-zA-Z0-9_./-]+@sha256:[0-9a-f]{64}$ ]] || { echo 'Expected a GHCR digest' >&2; exit 1; }
cd "$root"
exec 9>"$root/.deploy.lock"
flock -n 9 || { echo 'A deploy is already running' >&2; exit 1; }
secrets="$root/.observability.env"
[[ -f "$secrets" && -f "$root/apps/api/.env.prod" ]] || { echo 'Run the setup steps in deploy/OPS.md first' >&2; exit 1; }
git fetch origin "$sha"
release="$root/.releases/$sha"
mkdir -p "$release"
git archive "$sha" | tar -x -C "$release"
printf '%s\n' "$image" > "$release/.relay-image"
export RELAY_ENV_FILE="$root/apps/api/.env.prod"
previous=$(readlink "$root/.current" || true)
compose_infra() { docker compose -p infra --env-file "$secrets" -f "$1/deploy/infra/docker-compose.yml" "${@:2}"; }
compose_relay() { RELAY_IMAGE=$(cat "$1/.relay-image") docker compose -p poweur --env-file "$secrets" -f "$1/docker-compose.prod.yml" "${@:2}"; }
rollback() {
  trap - ERR
  echo 'Deployment failed; restoring previous release' >&2
  if [[ -n "$previous" && -f "$previous/.relay-image" ]]; then
    compose_infra "$previous" up -d --remove-orphans
    compose_infra "$previous" exec -T caddy caddy reload --config /etc/caddy/Caddyfile || true
    compose_relay "$previous" up -d --remove-orphans
    echo "Restored $previous" >&2
  else
    echo 'No recorded previous release. Inspect containers; see first-upgrade instructions.' >&2
  fi
  exit 1
}
# Validate and pull before changing any running container.
compose_infra "$release" config --quiet
compose_relay "$release" config --quiet
compose_infra "$release" pull
compose_relay "$release" pull
trap rollback ERR
compose_infra "$release" up -d --remove-orphans
compose_infra "$release" exec -T caddy caddy reload --config /etc/caddy/Caddyfile
compose_relay "$release" up -d --remove-orphans
# A network namespace curl checks services without publishing backend ports.
for attempt in $(seq 1 30); do
  if docker run --rm --network infra_net curlimages/curl:8.16.0 -fsS http://poweur-relay:8080/health > "$release/.health.json"; then
    if python3 -c 'import json,sys; d=json.load(open(sys.argv[1]));sys.exit(0 if d.get("versionHash")==sys.argv[2] and d.get("storage",{}).get("writable",True) else 1)' "$release/.health.json" "$sha"; then break; fi
  fi
  [[ $attempt -lt 30 ]] || false
  sleep 2
done
for url in http://infra-alloy:12345/-/ready http://infra-loki:3100/ready http://infra-prometheus:9090/-/ready http://infra-grafana:3000/api/health; do
  docker run --rm --network infra_net curlimages/curl:8.16.0 -fsS --retry 20 --retry-delay 2 --retry-all-errors "$url" >/dev/null
done
# An authenticated smoke identity is used only for inbox challenge/read, never
# registration or messaging, so deployments do not inflate growth metrics.
if [[ -f "$root/.smoke/config.toml" ]]; then
  docker run --rm --network infra_net -v "$root/.smoke:/root/.poweur" \
    --entrypoint /poweur-smoke "$image" inbox --json >/dev/null
else
  echo 'Missing .smoke identity; run OPS.md authenticated smoke setup' >&2
  false
fi
ln -sfn "$release" "$root/.current.next"
mv -Tf "$root/.current.next" "$root/.current"
printf '%s\n' "$previous" > "$root/.previous-release"
trap - ERR
echo "Deployed relay + observability from $sha"
