#!/usr/bin/env bash
# Restore a retained release without fetching/building or changing data volumes.
set -euo pipefail
root=${POWEUR_APP_DIR:-/opt/apps/poweur}
release=${1:?Usage: restore.sh RELEASE_DIRECTORY}
[[ $release == "$root/.releases/"* && -f "$release/.relay-image" ]] || { echo 'Invalid retained release' >&2; exit 1; }
export RELAY_ENV_FILE="$root/apps/api/.env.prod"
export RELAY_IMAGE=$(cat "$release/.relay-image")
docker compose -p infra --env-file "$root/.observability.env" -f "$release/deploy/infra/docker-compose.yml" up -d --remove-orphans
docker compose -p infra --env-file "$root/.observability.env" -f "$release/deploy/infra/docker-compose.yml" exec -T caddy caddy reload --config /etc/caddy/Caddyfile
export TELEMETRY_TRUSTED_PROXIES="$(docker inspect -f '{{(index .NetworkSettings.Networks "infra_net").IPAddress}}' infra-caddy)/32"
docker compose -p poweur --env-file "$root/.observability.env" -f "$release/docker-compose.prod.yml" up -d --remove-orphans
