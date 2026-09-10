#!/usr/bin/env bash
# Executed from the tested commit by Actions. No remote git pull/latest race.
# A failed health check leaves this release running. It does not compose an older stack.
set -euo pipefail
root=${POWEUR_APP_DIR:-/opt/apps/poweur}
sha=${1:?Usage: release.sh COMMIT IMAGE}
image=${2:?Usage: release.sh COMMIT IMAGE}
[[ $sha =~ ^[0-9a-f]{40}$ ]] || { echo 'Expected a full git commit' >&2; exit 1; }
[[ $image =~ ^ghcr.io/[a-zA-Z0-9_./-]+@sha256:[0-9a-f]{64}$ ]] || { echo 'Expected a GHCR digest' >&2; exit 1; }
command -v jq >/dev/null || { echo 'jq is required; see OPS.md' >&2; exit 1; }
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
[[ -f "$root/.smoke/config.toml" ]] || { echo 'Configure the smoke identity before deployment; see OPS.md' >&2; exit 1; }
compose_infra() { docker compose -p infra --env-file "$secrets" -f "$1/deploy/infra/docker-compose.yml" "${@:2}"; }
compose_relay() { RELAY_IMAGE=$(cat "$1/.relay-image") docker compose -p poweur --env-file "$secrets" -f "$1/docker-compose.prod.yml" "${@:2}"; }
compose_infra "$release" config --quiet
compose_relay "$release" config --quiet
compose_infra "$release" pull
compose_relay "$release" pull
compose_infra "$release" up -d --remove-orphans
compose_infra "$release" exec -T caddy caddy reload --config /etc/caddy/Caddyfile
# Trust only Caddy's current bridge address, never arbitrary forwarded headers.
export TELEMETRY_TRUSTED_PROXIES="$(docker inspect -f '{{(index .NetworkSettings.Networks "infra_net").IPAddress}}' infra-caddy)/32"
printf '%s\n' "$TELEMETRY_TRUSTED_PROXIES" > "$release/.trusted-proxies"
compose_relay "$release" up -d --remove-orphans
# Probe through the target container's network namespace. Joining infra_net
# as a one-shot curl container makes Docker DNS miss names for seconds at a
# time (`Resolving timed out after 2000 milliseconds`).
docker pull curlimages/curl:8.16.0 >/dev/null
probe_curl() {
  docker run --rm --network "container:$1" curlimages/curl:8.16.0 -fsS --connect-timeout 2 --max-time 5 "$2"
}
wait_http() {
  local container=$1 url=$2 attempts=${3:-90}
  for attempt in $(seq 1 "$attempts"); do
    if docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null | grep -qx true \
      && probe_curl "$container" "$url" >/dev/null; then
      return 0
    fi
    [[ $attempt -lt $attempts ]] || {
      echo "Timed out waiting for $container ($url)" >&2
      docker logs --tail 40 "$container" >&2 || true
      return 1
    }
    sleep 2
  done
}
for attempt in $(seq 1 60); do
  if probe_curl poweur-relay http://127.0.0.1:8080/health > "$release/.health.json"; then
    if jq -e --arg sha "$sha" '.versionHash == $sha and .storage.writable != false' "$release/.health.json" >/dev/null; then break; fi
  fi
  [[ $attempt -lt 60 ]] || { echo 'Timed out waiting for relay /health' >&2; docker logs --tail 40 poweur-relay >&2 || true; false; }
  sleep 2
done
wait_http infra-alloy http://127.0.0.1:12345/-/ready
wait_http infra-loki http://127.0.0.1:3100/ready
wait_http infra-prometheus http://127.0.0.1:9090/-/ready
wait_http infra-grafana http://127.0.0.1:3000/api/health
# An authenticated smoke identity is used only for inbox challenge/read, never
# registration or messaging, so deployments do not inflate growth metrics.
if [[ -f "$root/.smoke/config.toml" ]]; then
  # Identity create recorded an absolute host keys_dir (e.g. /tmp/poweur-smoke-home).
  # The container only sees the mount at /root/.poweur.
  smoke=$(mktemp -d)
  cp -a "$root/.smoke/." "$smoke/"
  {
    grep -v '^keys_dir[[:space:]]*=' "$smoke/config.toml" || true
    printf 'keys_dir = "/root/.poweur/keys"\n'
  } > "$smoke/config.toml.next"
  mv "$smoke/config.toml.next" "$smoke/config.toml"
  docker run --rm --network infra_net -v "$smoke:/root/.poweur" \
    --entrypoint /poweur-smoke "$image" inbox --json >/dev/null
  rm -rf "$smoke"
else
  echo 'Missing .smoke identity; run OPS.md authenticated smoke setup' >&2
  false
fi
ln -sfn "$release" "$root/.current.next"
mv -Tf "$root/.current.next" "$root/.current"
echo "Deployed relay + observability from $sha"
