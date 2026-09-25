#!/usr/bin/env bash
# Nightly encrypted backup of the Poweur production stack to a Hetzner Storage
# Box, with restic (run from its official image, so the host needs only Docker).
#
#   poweur-backup.sh run         back up now, prune to the retention window, check weekly
#   poweur-backup.sh init        create the restic repository (once)
#   poweur-backup.sh snapshots   list snapshots
#   poweur-backup.sh check       verify the repository (reads a 10 % data sample)
#   poweur-backup.sh restore <snapshot|latest> <host-dir>
#                                restore a snapshot into a directory on the host
#   poweur-backup.sh restic ...  any restic command against the repository
#
# Configuration: /etc/poweur-backup/backup.env (see backup.env.example), written
# by deploy/ansible. The SSH key, known_hosts and ssh config for the Storage Box
# live in /etc/poweur-backup/ssh; the repository password in
# /etc/poweur-backup/restic-password. Keep a copy of that password outside the
# server: without it the backups cannot be read.
#
# What is backed up (restore order and details: deploy/BACKUP.md):
#   - the relay's data volume (identities, files, spool, consent): read live,
#     file by file; the relay writes files whole, so no pause is needed
#   - the OAuth bridge's SQLite database: copied while the container is paused
#     for a few seconds, so the database and its WAL are consistent
#   - Postgres (Grafana's database): pg_dumpall
#   - Caddy's certificates and Grafana's state volume
#   - the stack's environment files (secrets), inside the encrypted repository
# Loki and Prometheus data are not backed up: they are 14/30-day telemetry.
set -Eeuo pipefail

CONF=${POWEUR_BACKUP_CONF:-/etc/poweur-backup/backup.env}
[ -f "$CONF" ] || { echo "poweur-backup: missing $CONF" >&2; exit 2; }
# shellcheck disable=SC1090
. "$CONF"

: "${RESTIC_REPOSITORY:?set RESTIC_REPOSITORY in $CONF, e.g. sftp:storagebox:poweur-restic}"
RESTIC_IMAGE=${RESTIC_IMAGE:-restic/restic:0.18.0}
CONF_DIR=${CONF_DIR:-/etc/poweur-backup}
PASSWORD_FILE=${PASSWORD_FILE:-$CONF_DIR/restic-password}
SSH_DIR=${SSH_DIR:-$CONF_DIR/ssh}
KEEP_WITHIN=${KEEP_WITHIN:-30d}
BACKUP_HOST=${BACKUP_HOST:-poweur-prod}
# "volume:name" pairs, mounted read-only at /backup/<name>.
BACKUP_VOLUMES=${BACKUP_VOLUMES:-"poweur_poweur_data:relay infra_caddy_data:caddy infra_grafana_data:grafana"}
OAUTH_CONTAINER=${OAUTH_CONTAINER:-poweur-oauth}
OAUTH_VOLUME=${OAUTH_VOLUME:-poweur_oauth_data}
POSTGRES_CONTAINER=${POSTGRES_CONTAINER:-infra-postgres}
POSTGRES_USER=${POSTGRES_USER:-postgres}
# Host files copied into the snapshot (space-separated; missing ones are skipped).
BACKUP_FILES=${BACKUP_FILES:-"/opt/apps/poweur/apps/api/.env.prod /opt/apps/poweur/.observability.env /opt/infra/.env /opt/apps/poweur-oauth/.env.prod"}
CHECK_WEEKDAY=${CHECK_WEEKDAY:-7} # ISO weekday for the weekly data check (7 = Sunday)
BACKUP_HEARTBEAT_URL=${BACKUP_HEARTBEAT_URL:-}
# Local repositories (tests): a host directory mounted at the same path.
REPO_HOST_DIR=${REPO_HOST_DIR:-}
# Docker network for the restic container (tests reach an SFTP container on it).
DOCKER_NETWORK=${DOCKER_NETWORK:-}

log() { echo "poweur-backup: $*" >&2; }

running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }

# restic in its container, with the repository credentials and any extra args.
restic_run() {
  local extra=()
  [ -n "$REPO_HOST_DIR" ] && extra+=(-v "$REPO_HOST_DIR:$REPO_HOST_DIR")
  [ -n "$DOCKER_NETWORK" ] && extra+=(--network "$DOCKER_NETWORK")
  docker run --rm -i \
    -e RESTIC_REPOSITORY="$RESTIC_REPOSITORY" \
    -e RESTIC_PASSWORD_FILE=/run/restic-password \
    -e RESTIC_CACHE_DIR=/cache \
    -v "$PASSWORD_FILE:/run/restic-password:ro" \
    -v "$SSH_DIR:/root/.ssh:ro" \
    -v poweur_backup_cache:/cache \
    --hostname "$BACKUP_HOST" \
    ${extra[@]+"${extra[@]}"} ${RESTIC_MOUNTS[@]+"${RESTIC_MOUNTS[@]}"} \
    "$RESTIC_IMAGE" "$@"
}
RESTIC_MOUNTS=()

heartbeat() {
  [ -n "$BACKUP_HEARTBEAT_URL" ] || return 0
  curl -fsS -m 10 --retry 3 "$BACKUP_HEARTBEAT_URL$1" >/dev/null || log "heartbeat $1 failed"
}

# Postgres dump, OAuth database copy and env files are staged in a Docker volume,
# never on the host's disk, and the volume is emptied after every run.
STAGING_VOLUME=${STAGING_VOLUME:-poweur_backup_staging}
PAUSED=""
cleanup() {
  if [ -n "$PAUSED" ]; then docker unpause "$PAUSED" >/dev/null 2>&1 || true; fi
  docker volume rm -f "$STAGING_VOLUME" >/dev/null 2>&1 || true
}

# A shell in the restic image with the staging volume at /staging.
staging_sh() {
  docker run --rm -i --entrypoint sh -v "$STAGING_VOLUME:/staging" "$@"
}

stage() {
  docker volume rm -f "$STAGING_VOLUME" >/dev/null 2>&1 || true
  docker volume create "$STAGING_VOLUME" >/dev/null
  staging_sh "$RESTIC_IMAGE" -c 'mkdir -p /staging/oauth /staging/config && chmod 700 /staging'

  if running "$POSTGRES_CONTAINER"; then
    log "dumping postgres"
    docker exec "$POSTGRES_CONTAINER" pg_dumpall -U "$POSTGRES_USER" | gzip -c |
      staging_sh "$RESTIC_IMAGE" -c 'cat > /staging/postgres.sql.gz'
  else
    log "postgres container $POSTGRES_CONTAINER not running; skipped"
  fi

  if docker volume inspect "$OAUTH_VOLUME" >/dev/null 2>&1; then
    log "copying the OAuth bridge database"
    if running "$OAUTH_CONTAINER"; then
      docker pause "$OAUTH_CONTAINER" >/dev/null
      PAUSED=$OAUTH_CONTAINER
    fi
    staging_sh -v "$OAUTH_VOLUME:/src:ro" "$RESTIC_IMAGE" -c 'cp -a /src/. /staging/oauth/'
    if [ -n "$PAUSED" ]; then
      docker unpause "$PAUSED" >/dev/null
      PAUSED=""
    fi
  fi

  local files=()
  for file in $BACKUP_FILES; do
    [ -f "$file" ] && files+=("$file")
  done
  if [ "${#files[@]}" -gt 0 ]; then
    log "adding ${#files[@]} environment file(s)"
    COPYFILE_DISABLE=1 tar -cf - "${files[@]}" 2>/dev/null | staging_sh "$RESTIC_IMAGE" -c 'tar -xf - -C /staging/config'
  fi
  return 0
}

cmd_run() {
  trap 'log "FAILED (line $LINENO)"; cleanup; heartbeat /fail' ERR
  trap cleanup EXIT
  stage

  RESTIC_MOUNTS=(-v "$STAGING_VOLUME:/backup/staging:ro")
  local paths=(/backup/staging)
  for pair in $BACKUP_VOLUMES; do
    local volume=${pair%%:*} name=${pair##*:}
    if docker volume inspect "$volume" >/dev/null 2>&1; then
      RESTIC_MOUNTS+=(-v "$volume:/backup/$name:ro")
      paths+=("/backup/$name")
    else
      log "volume $volume not found; skipped"
    fi
  done

  log "backing up ${paths[*]}"
  restic_run backup --host "$BACKUP_HOST" --tag nightly "${paths[@]}"
  RESTIC_MOUNTS=()

  log "pruning snapshots older than $KEEP_WITHIN"
  restic_run forget --host "$BACKUP_HOST" --group-by host,tags --keep-within "$KEEP_WITHIN" --prune

  if [ "$(date +%u)" = "$CHECK_WEEKDAY" ]; then
    log "weekly check (10 % of the data)"
    restic_run check --read-data-subset=10%
  fi

  heartbeat ""
  log "done"
}

cmd_init() {
  if restic_run cat config >/dev/null 2>&1; then
    log "repository already initialised"
  else
    restic_run init
  fi
}

cmd_restore() {
  local snapshot=${1:?snapshot id or "latest"} target=${2:?host directory to restore into}
  mkdir -p "$target"
  RESTIC_MOUNTS=(-v "$(cd "$target" && pwd):/restore")
  restic_run restore "$snapshot" --target /restore
  log "restored $snapshot into $target (see deploy/BACKUP.md to put it back)"
}

case "${1:-}" in
  run) cmd_run ;;
  init) cmd_init ;;
  snapshots) restic_run snapshots ;;
  check) restic_run check --read-data-subset=10% ;;
  restore) shift; cmd_restore "$@" ;;
  restic) shift; restic_run "$@" ;;
  *) sed -n '2,13p' "$0" >&2; exit 2 ;;
esac
