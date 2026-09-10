#!/usr/bin/env bash
set -euo pipefail
root=${POWEUR_APP_DIR:-/opt/apps/poweur}
previous=$(cat "$root/.previous-release")
[[ -f "$previous/.relay-image" ]] || { echo 'No previous release recorded' >&2; exit 1; }
exec "$previous/deploy/release.sh" "$(basename "$previous")" "$(cat "$previous/.relay-image")"
