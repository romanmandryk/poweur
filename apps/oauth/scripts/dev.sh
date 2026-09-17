#!/usr/bin/env bash
# A local relay (:8089, identities under *.localhost, web app at /app/) and the
# bridge (http://oauth.localhost:8090 — the sign-in audience needs a dotted host) for trying the OAuth journeys by hand. A public demo client
# "demo" returns to http://127.0.0.1:9999/callback.
#
#   apps/oauth/scripts/dev.sh
#   # create an identity: HOME=$(mktemp -d) POWEUR_RESOLVER_SCHEME=http RESOLVER_ALLOW_PRIVATE=1 \
#   #   POWEUR_RESOLVER_DIAL=127.0.0.1:8089 go run ./apps/cli identity create alice.localhost \
#   #   --hosted --relay http://127.0.0.1:8089
set -euo pipefail
root=$(cd "$(dirname "$0")/../../.." && pwd)
data=${POWEUR_OAUTH_DEV_DATA:-${TMPDIR:-/tmp}/poweur-oauth-dev}
mkdir -p "$data"
[ -s "$data/kek" ] || (cd "$root/apps/oauth" && go run ./cmd/poweur-oauth gen-key > "$data/kek")

cat > "$data/clients.json" <<JSON
{"clients":[{"client_id":"demo","client_name":"Demo application",
  "redirect_uris":["http://127.0.0.1:9999/callback"],"token_endpoint_auth_method":"none"}]}
JSON

(cd "$root/apps/api" && LISTEN_ADDR=:8089 RELAY_ADDRESS=localhost:8089 RELAY_SCHEME=http \
  POWEUR_DATA="$data/relay" HOSTED_DOMAINS=localhost RESOLVER_ALLOW_PRIVATE=1 \
  WEB_STATIC_DIR="$root/apps/web/dist" OAUTH_BRIDGE_URL=http://oauth.localhost:8090 go run .) &
relay=$!
trap 'kill $relay 2>/dev/null || true' EXIT

cd "$root/apps/oauth"
OAUTH_ISSUER=http://oauth.localhost:8090 OAUTH_ADDR=:8090 OAUTH_DATABASE="$data/oauth.db" \
  OAUTH_KEY_ENCRYPTION_KEY_FILE="$data/kek" OAUTH_DEFAULT_SIGNER=http://localhost:8089/app/ \
  OAUTH_LAUNCHER_URL=http://localhost:8089 OAUTH_LAUNCHER_DOMAIN=localhost \
  OAUTH_STATIC_CLIENTS="$data/clients.json" OAUTH_ABUSE_CONTACT=abuse@localhost \
  RESOLVER_ALLOW_PRIVATE=1 POWEUR_RESOLVER_SCHEME=http OAUTH_RESOLVER_DIAL=127.0.0.1:8089 \
  go run ./cmd/poweur-oauth
