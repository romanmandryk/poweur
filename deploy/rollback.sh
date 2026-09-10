#!/usr/bin/env bash
set -euo pipefail
root=${POWEUR_APP_DIR:-/opt/apps/poweur}
exec 9>"$root/.deploy.lock"
flock -n 9 || { echo 'A deploy is already running' >&2; exit 1; }
previous=$(cat "$root/.previous-release")
current=$(readlink "$root/.current")
bash "$(dirname "$0")/restore.sh" "$previous"
ln -sfn "$previous" "$root/.current.next"
mv -Tf "$root/.current.next" "$root/.current"
printf '%s\n' "$current" > "$root/.previous-release"
echo "Restored $previous; data volumes retained"
