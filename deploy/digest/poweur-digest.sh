#!/usr/bin/env bash
# Daily growth digest: one ntfy push with how much Poweur grew in the last 24h.
# Quiet on days nothing grew. Runs from poweur-digest.timer on the server and
# asks the private Prometheus (infra-prometheus) over `docker exec`.
#
#   poweur-digest            send if anything grew
#   poweur-digest --dry-run  print the message, send nothing
#   poweur-digest --force    send even when nothing grew (use to test the push)
#
# Settings come from /etc/poweur-digest/digest.env (written by deploy/ansible):
#   NTFY_URL=https://ntfy.sh/<secret-topic>
# Tests replace the Prometheus call with PROM_QUERY_CMD (query as $1, prints
# the instant-query JSON).
set -euo pipefail

ENV_FILE=${DIGEST_ENV:-/etc/poweur-digest/digest.env}
[ -r "$ENV_FILE" ] && . "$ENV_FILE"

mode=send
case "${1:-}" in
  --dry-run) mode=dry ;;
  --force) mode=force ;;
  "") ;;
  *) echo "usage: poweur-digest [--dry-run|--force]" >&2; exit 2 ;;
esac

prom() {
  if [ -n "${PROM_QUERY_CMD:-}" ]; then
    "$PROM_QUERY_CMD" "$1"
  else
    docker exec infra-prometheus wget -qO- \
      "http://localhost:9090/api/v1/query?query=$(jq -rn --arg q "$1" '$q|@uri')"
  fi
}

# value EXPR -> an integer; a missing series (nothing happened yet) is 0.
value() {
  local v
  v=$(prom "$1" | jq -r '.data.result[0].value[1] // "0"')
  awk -v v="$v" 'BEGIN { printf "%d", (v == "NaN" ? 0 : v + 0.5) }'
}

# commas N -> 1,234
commas() {
  local n=$1 out=
  while [ "${#n}" -gt 3 ]; do out=",${n: -3}$out"; n=${n:0:${#n}-3}; done
  echo "$n$out"
}

prod='deployment_environment_name="production"'
inc() { echo "sum(increase($1[$2]))"; }

ids_total=$(value "max(poweur_state{state=\"hosted_identities\",$prod})")
ids_new=$(value "$(inc "poweur_actions_total{action=\"registration.create\",outcome=\"success\",$prod}" 24h)")

# name|selector. "today" is the last 24h; "30d" is the last 30 days, which is
# as far back as Prometheus keeps data (counters restart at 0 with the apps).
series=(
  "Messages|poweur_actions_total{action=~\"message.submit|message.anonymous\",outcome=\"success\",$prod}"
  "Relay sign-ins (CLI + web)|poweur_actions_total{action=\"session.create\",outcome=\"success\",$prod}"
  "OAuth sign-ins|poweur_oauth_events_total{event=\"authorize.code_issued\"}"
  "Guestbook sign-ins|poweur_guestbook_signins_total{result=\"completed\",$prod}"
  "Guestbook entries|poweur_guestbook_posts_total{result=\"created\",$prod}"
  "Hello bot messages|poweur_hello_messages_total{$prod}"
)

grew=$ids_new
lines=("IDs: $(commas "$ids_total") total, +$(commas "$ids_new") today")
for s in "${series[@]}"; do
  name=${s%%|*}
  sel=${s#*|}
  d=$(value "$(inc "$sel" 24h)")
  m=$(value "$(inc "$sel" 30d)")
  grew=$((grew + d))
  lines+=("$name: +$(commas "$d") today, $(commas "$m") in 30d")
done

msg=$(printf '%s\n' "${lines[@]}")

if [ "$mode" = dry ]; then
  echo "$msg"
  exit 0
fi
if [ "$grew" -le 0 ] && [ "$mode" != force ]; then
  echo "poweur-digest: nothing grew in the last 24h, not sending"
  exit 0
fi
: "${NTFY_URL:?NTFY_URL is not set (see $ENV_FILE)}"
curl -fsS --max-time 20 --retry 3 --retry-delay 5 \
  -H "Title: Poweur growth, $(date -u +%F)" -H "Tags: chart_with_upwards_trend" \
  -d "$msg" "$NTFY_URL" >/dev/null
echo "poweur-digest: sent"
