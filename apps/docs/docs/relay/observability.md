# Observability and analytics preference

The relay optionally pushes OTLP/HTTP protobuf logs and metrics to an independent intake service. The bundled deployment uses Alloy, Prometheus, Loki and Grafana. It does not collect message bodies, file contents/names, keys, tokens, cookies or raw URLs.

## Configuration

| Environment | Default | Purpose |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | Primary OTLP sink (Grafana Alloy); base URL receiving `/v1/logs` and `/v1/metrics` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` | Only supported protocol |
| `OTEL_EXPORTER_OTLP_HEADERS` | empty | Comma-separated header=value pairs; percent-encode reserved characters |
| `TELEMETRY_OTLP_SECONDARY_ENDPOINT` | empty | Optional second OTLP sink (Better Stack collector, another vendor). Same privacy filter as the primary. Unset to stop dual-export |
| `TELEMETRY_OTLP_SECONDARY_HEADERS` | empty | Headers for the secondary sink |
| `TELEMETRY_UPTIME_URL` | empty | Optional GET heartbeat (Better Stack Heartbeat URL, or any probe URL) |
| `BETTERSTACK_RUM_TOKEN` | empty | Public JavaScript-tag token; served at `GET /app/observability.json`. Empty loads no browser tag |
| `TELEMETRY_HASH_KEY` | empty | Required when exporting; at least 32 bytes, generated independently of identity keys |
| `TELEMETRY_ALLOW_HTTP` | `0` | Explicit override for an isolated same-VM bridge or tests; use HTTPS remotely |
| `TELEMETRY_TRUSTED_PROXIES` | empty | Comma-separated CIDRs; trust X-Forwarded-For only through these peers |
| `TELEMETRY_ENVIRONMENT` | `production` | `production`, `development`, or `test`; public queries select production |
| `LOG_LEVEL` | `info` | Diagnostic threshold; structured business events still export at higher levels |

Empty primary and secondary endpoints disable outbound OTLP, including signal-specific SDK environment overrides. Local JSON logging remains. Without a configured hash key, local-only pseudonyms use a random process key. Never put credentials in the endpoint URL. Delivery is bounded and best effort; collector failure does not change request success. Queue overflow/export loss is counted. No tracing or user-level metrics labels are enabled.

The web client (and the Capacitor shell wrapping the same bundle) reads `GET /app/observability.json` (no-store). That file lists browser providers; an empty `providers` array loads no third-party tag. The Better Stack JavaScript tag then collects unhandled errors, web vitals, and behavioral autocapture (clicks, rage clicks, forms). The app also reports React render errors and SPA screen names. Do not identify users or send URL hashes (claim fragments carry keys). Session replay for a messenger should stay off in the vendor UI. Native iOS/Android process crashes are outside this tag.

## Per-identity preference

The existing authenticated DAV tree stores `poweur-sys/relay/analytics.json`:

```json
{"version":1,"granted":false,"updated_at":"2026-09-10T12:00:00Z"}
```

The document is limited to 4096 bytes and validated by the relay. Writes require the existing owner-authorized DAV session/signature flow. Missing, invalid, deleted or false preferences mean detailed analytics is off. This adds a system document, not a new authentication header or signing format.

Web/native Settings → Detailed relay analytics changes it for the active identity. Both Go and TS CLIs support `poweur analytics show|on|off [--use-identity=...] [--json]`. The JS SDK exposes `client.analyticsPreference()` and `client.setAnalyticsConsent(boolean)`.

| Record field | Off / unknown | On |
|---|---|---|
| Authenticated actor | HMAC-SHA256 pseudonym | Canonical identity |
| Client IP | Omitted | Included for that actor's direct request |
| Timestamp, action, outcome, duration | Included | Included |

Unauthenticated/failed-auth callers have neither actor nor IP. Forwarded/background actions never treat a relay peer's IP as a user's IP. Other participants are omitted. Consent is not propagated across relays. The same transformation occurs before local logging and outbound export; queued raw records are rechecked before network export. Turning off does not delete already exported history. Hashes are pseudonymous, not anonymous, and represent identities rather than unique people.

## Event inventory and queries

Every relay HTTP route produces `kind=request`, `action=http.request`, a fixed route template, bounded method, status, outcome and duration. Unknown routes become `unmatched`. Authenticated actor tagging occurs only after verification. Expected 4xx outcomes are rejections; 5xx/panics also produce sanitized diagnostic events with static error codes and function-only stack frames.

Business actions cover registration, session create/revoke, identity export/rotation/encryption key, DAV tokens, keystore and enrollment, message submit/receive/anonymous/enqueue/pickup/consume, contact queue/pickup, acknowledgments, forwarding, upload create/chunk/complete/cancel, contacts/policy/shares/groups and analytics preference writes. DAV reads, sync reads, identity availability/resolution and other routes retain HTTP events. SSE adds open/close events; startup/shutdown, spool/ack expiry and storage sampling failures have background hooks. Never derive new action names from input or paths.

OTLP log bodies contain JSON `timestamp`, `kind`, `action`, `outcome`, optional `error_code`, `route`, `method`, `status`, `duration_ms`, `actor_id`, `identity_mode`, `client_ip` and `stack`. Resource attributes identify service, release version and environment. Loki indexes service/environment only; private Explore can parse `| json` to filter actor fields. HTTP and business events are distinct; count only successful `message.submit`/`message.anonymous` for origin submissions, not receive, forward, polls or pickup.

Business actions may carry a bounded `detail`, never derived from free-form input:

| Action | `detail` values |
|---|---|
| `message.submit`, `message.receive` | `chat` (absent type or `chat.text`), a registered `sys.*` type, `sys.other`, or `app` for every application type |
| `message.anonymous` | `anonymous` |
| `settings.change` | `profile.display_name`, `profile.avatar`, `profile.bio`, `profile.links`, `profile.locale`, `inbox.mode`, `inbox.anonymous`, `inbox.read_receipts`, `analytics.granted` |

`settings.change` is emitted once per field whose value differs from the stored document, and only when the validated DAV PUT succeeds; saving an unchanged value and unknown fields are not counted. Values themselves are never recorded. Message types come from the plaintext envelope; payloads stay encrypted and are never read.

Every five minutes the relay also samples adoption across hosted identities into `poweur_state{state="adopt_*"}`: profile fields set (display name, bio, avatar, links, locale), inbox policy mode (or `adopt_inbox_default` when never set), anonymous inbox enabled, read receipts off, detailed analytics granted and non-empty contacts. Only the public profile and relay-readable settings are consulted, never `poweur-sys/private`, and only totals are exported.

Metrics: `poweur_http_requests_total`, HTTP duration histogram, `poweur_actions_total` (labels `action`, `detail`, `outcome`), `poweur_state` (identities/inbox/storage/adoption), `poweur_telemetry_dropped`, and heartbeat timestamp. Counters/histograms have bounded labels only, with no identities/IPs/paths. Public recording rules compute increases per source series before summing, so process resets do not become growth. Public counts are estimates with possible export gaps.

For deployment, DNS, private/public sharing, retention and recovery, see the repository's `deploy/OPS.md` runbook.
