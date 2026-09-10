#!/usr/bin/env bash
# Generate local secrets without evaluating dotenv contents or printing credentials.
set -euo pipefail
umask 077
target=.observability.env
original=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --file) target=${2:?Missing --file path}; shift 2 ;;
    --import-env) original=${2:?Missing --import-env path}; shift 2 ;;
    *) echo 'Usage: setup-observability.sh [--file PATH] [--import-env PATH]' >&2; exit 1 ;;
  esac
done
command -v openssl >/dev/null || { echo 'openssl is required' >&2; exit 1; }
mkdir -p "$(dirname "$target")"
tmp=$(mktemp "${target}.tmp.XXXXXX")
trap 'rm -f "$tmp"' EXIT
if [[ -f "$target" ]]; then
  cat "$target" > "$tmp"
elif [[ -n "$original" ]]; then
  cat "$original" > "$tmp"
fi
has() { grep -Eq "^[[:space:]]*(export[[:space:]]+)?${1}[[:space:]]*=" "$tmp"; }
password_present=false
headers_present=false
if has OTLP_HTPASSWD; then password_present=true; fi
if has OTEL_EXPORTER_OTLP_HEADERS; then headers_present=true; fi
if [[ $password_present != "$headers_present" ]]; then
  echo 'Both intake credential fields must be present or absent; refusing mismatched credentials' >&2
  exit 1
fi
append() {
  if ! has "$1"; then
    # All new values below are generated constants without apostrophes. Existing
    # dotenv contents are copied verbatim, never sourced as shell commands.
    printf "\n%s='%s'\n" "$1" "$2" >> "$tmp"
  fi
}
password=$(openssl rand -hex 32)
digest=$(printf '%s' "$password" | openssl dgst -sha1 -binary | openssl base64 -A)
auth=$(printf 'relay:%s' "$password" | openssl base64 -A)
append POSTGRES_PASSWORD "$(openssl rand -hex 32)"
append GRAFANA_DB_PASSWORD "$(openssl rand -hex 32)"
append GRAFANA_PASSWORD "$(openssl rand -hex 24)"
append OTLP_HTPASSWD "relay:{SHA}$digest"
append OTEL_EXPORTER_OTLP_HEADERS "Authorization=Basic $auth"
append OTEL_EXPORTER_OTLP_ENDPOINT 'http://infra-alloy:4318'
append TELEMETRY_ALLOW_HTTP 1
append TELEMETRY_HASH_KEY "$(openssl rand -hex 32)"
append ALERT_EMAIL admin@poweur.net
append GRAFANA_SMTP_ENABLED false
chmod 600 "$tmp"
mv -f "$tmp" "$target"
echo "Wrote $target (0600); existing settings preserved. Configure SMTP before expecting email alerts."
