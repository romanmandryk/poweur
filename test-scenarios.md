# Poweur ID Test Scenarios

This document defines the end-to-end scenarios needed to validate the Poweur ID system across the relay, CLI, documentation, infrastructure, and mobile apps.

It is intentionally split by automation level:

- `Automated`: should run in CI without human involvement.
- `Mostly automated`: can run in CI or a scripted environment, but may require external DNS credentials or ephemeral infrastructure.
- `Manual`: requires a human, usually because of passkey or mobile platform approval UX.

## Scope And Assumptions

Current repository status matters for planning:

- `apps/api` is only a minimal HTTP stub today.
- `apps/cli` is still a placeholder.
- `apps/ios` and `apps/android` are scaffolds, not full native apps yet.

The scenarios below are therefore acceptance targets for the intended system, not a claim that the current repo already passes them.

## Test Environments

### Local automated environment

Use for relay, CLI, docs, and protocol tests.

- Two relay instances running locally on different ports
- A local DNS resolver or test DNS server (for example CoreDNS) with programmable records
- Two test identities such as `alice.test` and `bob.test`
- One verifier test app that simulates third-party sign-up and sign-in requests

### Provider-backed integration environment

Use for DNS provider and infra validation.

- Cloudflare test zone
- Hetzner DNS test zone
- Optional Hetzner Cloud staging project

### Manual device lab

Use for native mobile and passkey validation.

- One iPhone with biometrics enabled
- One Android device with biometrics enabled
- Desktop browser for QR-based auth flow
- Test websites or apps acting as relying parties

## Scenario Matrix

| ID | Area | Scenario | Automation |
|----|------|----------|------------|
| DOC-01 | Docs | Build the Docusaurus docs successfully | Automated |
| DOC-02 | Docs | Verify all internal doc links and sidebar entries resolve | Automated |
| API-01 | Relay | `GET /health` returns `200` and protocol version metadata | Automated |
| API-02 | Relay | `POST /identities` writes public key and routing DNS records | Mostly automated |
| API-03 | Relay | Invalid DNS token returns provider write failure | Mostly automated |
| API-04 | Relay | Valid signed local message is accepted and stored for recipient | Automated |
| API-05 | Relay | Invalid signature is rejected with `401` | Automated |
| API-06 | Relay | Oversized payload is rejected with `413` before verification | Automated |
| API-07 | Relay | Rate limits trigger `429` before expensive work | Automated |
| API-08 | Relay | Remote message routing works across two relays using DNS lookup | Automated |
| API-09 | Relay | DNS cache honors TTL expiry and refreshes routing | Automated |
| API-10 | Relay | `GET /auth/challenge` issues a short-lived challenge and invalidates it after use | Automated |
| API-11 | Relay | `GET /messages/:identity` returns inbox after valid signed challenge | Automated |
| CLI-01 | CLI | Create local identity and output machine-readable JSON | Automated |
| CLI-02 | CLI | Show current identity and public key | Automated |
| CLI-03 | CLI | Send a signed, end-to-end encrypted message through the relay | Automated |
| CLI-04 | CLI | Read inbox through challenge-response flow and decrypt payloads | Automated |
| CLI-05 | CLI | Sign third-party verifier request in headless mode | Automated |
| SESS-01 | Session | `POST /sessions` accepts a valid identity-signed session registration and issues a session id bounded by 24h TTL | Automated |
| SESS-02 | Session | Session registration with a forged identity signature returns `401` | Automated |
| SESS-03 | Session | Session registration exceeding the 24h TTL returns `400` | Automated |
| SESS-04 | Session | Message signed with an expired/unknown session returns `401 session_expired` | Automated |
| SESS-05 | Session | CLI transparently re-registers a session on `401 session_expired` and retries once | Automated |
| SESS-06 | Session | `DELETE /sessions/:id` revokes a session; subsequent messages using that id are rejected | Automated |
| SESS-07 | Session | Cross-relay message carrying a valid `session_proof` is verified by a relay that did not issue the session | Automated |
| E2E-01 | Crypto | End-to-end encrypted payload roundtrips between sender and recipient CLI | Automated |
| E2E-02 | Crypto | Relay never sees plaintext: inbox ciphertext matches exactly what the client sent | Automated |
| E2E-03 | Crypto | Sending to a recipient without a published encryption key is refused by all clients (CLI aborts with an actionable error; mobile shows the same hard error) and, if bypassed, rejected by the relay with `400 encryption_required` | Automated |
| E2E-04 | Crypto | Tampering with the ciphertext after signing invalidates the signature | Automated |
| E2E-05 | Crypto | Identity creation publishes both `_poweur.<id>` and `_poweur-enc.<id>` TXT records | Mostly automated |
| AUTH-01 | Auth | Website sign-up request is approved in mobile app via QR | Manual |
| AUTH-02 | Auth | Website sign-in request is approved in mobile app via QR | Manual |
| AUTH-03 | Auth | Same-device deep-link sign-in works from mobile browser to app and back | Manual |
| AUTH-04 | Auth | Expired or replayed auth request is rejected by verifier | Automated |
| AUTH-05 | Auth | Verifier can resolve identity from DNS and validate signature | Automated |
| AUTH-06 | Auth | Verifier can consume `/.well-known/poweur.json` metadata | Automated |
| AUTH-07 | Auth | Optional `did:web` document is served and matches DNS key material | Automated |
| MOBILE-01 | iOS | Create identity with passkey-backed keypair (Ed25519) + X25519 encryption keypair, verify DNS propagation of both TXT records | Manual |
| MOBILE-02 | Android | Create identity with passkey-backed keypair + X25519 encryption keypair, verify DNS propagation of both TXT records | Manual |
| MOBILE-03 | iOS | Approve third-party sign-up request (passkey) and return signed response | Manual |
| MOBILE-04 | Android | Approve third-party sign-in request (passkey) and return signed response | Manual |
| MOBILE-05 | Mobile | Send and receive end-to-end encrypted messages between two mobile identities | Manual |
| MOBILE-06 | Mobile | Session registration on first send triggers exactly one passkey prompt, and subsequent sends within 24h trigger none | Manual |
| MOBILE-07 | Mobile | When a session expires, the next send triggers a single passkey prompt to re-register | Manual |
| MOBILE-08 | Mobile | Client refuses to send to a recipient without a published encryption key, and surfaces a clear error (encryption is mandatory; plaintext is never emitted) | Manual |
| INT-01 | Integration | `identity create` via the CLI publishes signing, encryption, and host records into the shared zone | Automated |
| INT-02 | Integration | End-to-end encrypted message round-trip on a single relay | Automated |
| INT-03 | Integration | Encrypted message forwards across two relays; receiving relay verifies the session proof against DNS-published identity key | Automated |
| INT-04 | Integration | Locally-expired session triggers a transparent re-registration on the next send, producing a fresh session id | Automated |
| INT-05 | Integration | Missing recipient encryption key surfaces a clear `[encrypted: no local encryption key]` marker instead of leaking ciphertext as prose | Automated |
| INT-06 | Integration | Replayed signed message is rejected on the second submission | Pending (test skipped until dedup lands) |
| INT-07 | Integration | `POST /messages` with a forged signature returns `401` and does not reach any inbox | Automated |
| INT-08 | Integration | Tampering with the DNS `_poweur.<id>` TXT record breaks cross-relay verification on the receiving relay; forwarding fails and the attacker's payload is never delivered | Automated |
| INT-09 | Integration | Full two-way conversation: both users create identities, auto-register sessions on first send, exchange an Alice→Bob message and a Bob→Alice reply, both sides decrypt successfully | Automated |
| INT-10 | Integration | Retrofit: a legacy identity without `_poweur-enc.<id>` cannot receive messages (sender aborts / relay returns `400 encryption_required`); after `poweur identity add-encryption-key`, the same recipient accepts and decrypts a fresh send | Automated |
| INT-IDSIGN-01 | Integration | `poweur send --sign-with=identity` on a single relay: message accepted, recipient decrypts, and sender's local session file is NOT created | Automated |
| INT-IDSIGN-02 | Integration | `poweur send --sign-with=identity` across two relays: receiving relay verifies the identity signature using the sender's DNS-published signing key; recipient decrypts | Automated |
| INT-IDSIGN-03 | Integration | Forged identity-signed envelope (empty `session_id`, signature under an unknown key) is rejected with `401` and never reaches the recipient's inbox | Automated |
| SEND-01 | Integration | Default send (no `--via-home-relay`) goes straight from Alice's CLI to Bob's relay; Alice's home relay sees zero `POST /messages` traffic on the send path | Automated |
| SEND-02 | Integration | `--via-home-relay` posts to Alice's home relay, which accepts (sender-local) and forwards to Bob's relay; both relays see exactly one `POST /messages` | Automated |
| FWD-01 | Integration | Note-to-self (sender == recipient, both local on the same relay) is accepted and stored in the sender's own inbox | Automated |
| FWD-02 | Integration | A structurally-valid encrypted envelope POSTed directly to a third relay where neither party is locally hosted is rejected with `403 not_authorized` | Automated |
| FWD-03 | Integration | The same at-least-one-local rule applies to `POST /acks`: an ack posted to a relay where neither party is local is rejected with `403 not_authorized` | Automated |
| DLV-01 | Integration | After a successful default send, the sender's pending journal records tick 1 (`delivered_recipient_relay`) for that message id | Automated |
| DLV-02 | Integration | After the recipient drains their inbox, an ack flows back to the sender's home relay; the sender's next `inbox` poll advances the journal to tick 2 (`delivered_client`) | Automated |
| DLV-03 | Integration | If the recipient never polls, no ack is generated and the sender's journal stays pinned at tick 1 regardless of how many times the sender polls their own inbox | Automated |
| DLV-04 | Integration | A `POST /acks` body whose signature was produced by a key nobody in the system holds is rejected with `401 unauthorized`; the sender's journal does NOT advance to tick 2 | Automated |
| ADMIN-01 | Integration | `POST /identities` without an identity-signed admin envelope is rejected with `400 invalid_request`; no DNS write happens | Automated |
| ADMIN-02 | Integration | `POST /identities` whose identity_signature was produced by a different key than the one in `public_key` is rejected with `401 unauthorized` | Automated |
| ADMIN-03 | Integration | `POST /identities/:identity/encryption-key` without an identity-signed admin envelope is rejected with `400 invalid_request` | Automated |
| ADMIN-04 | Integration | `POST /identities/:identity/encryption-key` whose identity_signature does not match the registered identity's signing key is rejected with `401 unauthorized` | Automated |
| ADMIN-05 | Integration | `DELETE /sessions/:id` without an identity-signed admin envelope is rejected with `400 invalid_request` | Automated |
| ADMIN-06 | Integration | `DELETE /sessions/:id` whose identity_signature does not match the claimed identity's signing key is rejected with `401 unauthorized`, even with a plausible session id | Automated |
| INFRA-01 | Infra | Terraform plan succeeds with staging variables | Automated |
| INFRA-02 | Infra | Terraform apply creates relay host, LB, DNS, and TLS resources in staging | Mostly automated |
| INFRA-03 | Infra | Deployed relay passes health check behind load balancer | Mostly automated |

## Detailed Scenarios

### DOC-01: Documentation build

Goal: keep the docs site as a reliable source of truth.

Steps:

1. Install workspace dependencies.
2. Build the docs site in `apps/docs`.
3. Fail if Markdown frontmatter, sidebar registration, or imports are invalid.

Expected result:

- Build completes successfully.

### API-02: Identity registration writes DNS

Goal: prove that relay registration can create the minimum viable identity records.

Steps:

1. Start relay with test DNS provider credentials or a programmable fake provider.
2. Submit `POST /identities` with a new identity and public key.
3. Query DNS for `_poweur.<identity>` and `<identity>`.

Expected result:

- TXT public key record exists and matches the submitted public key.
- Routing record exists and points at the expected relay address.

### API-08: Cross-relay message routing

Goal: prove that routing is DNS-driven rather than registry-driven.

Steps:

1. Start relay A and relay B.
2. Register `alice.test` on relay A and `bob.test` on relay B.
3. Send a signed message from Alice to Bob via relay A.
4. Fetch Bob's inbox from relay B.

Expected result:

- Relay A resolves Bob through DNS.
- Relay B verifies Alice's signature independently.
- Bob receives the original signed envelope unchanged.

### CLI-05: Headless auth signing

Goal: support bot and agent participation in third-party auth flows.

Steps:

1. Create or load a CLI-managed identity.
2. Provide a verifier request object to the CLI.
3. Have the CLI inspect and sign the request.
4. Submit the signed result to the verifier test app.

Expected result:

- CLI outputs a signed auth response in JSON.
- Verifier accepts the response and ties it to the original nonce and expiry.

### AUTH-01 / AUTH-02: Third-party sign-up and sign-in

Goal: prove that the DNS identity is usable beyond messaging.

Steps:

1. Open a relying-party website in a desktop browser.
2. Start a sign-up or sign-in flow that displays a QR code.
3. Scan the QR code with the Poweur ID mobile app.
4. Review verifier domain, requested action, and statement.
5. Approve with biometrics or passkey confirmation.
6. Return to the website and verify completion.

Expected result:

- The website receives a signed response for the selected identity.
- Signature verification succeeds against DNS-published key material.
- Replay of the same signed request is rejected.

### AUTH-07: `did:web` compatibility

Goal: ensure Poweur ID can interoperate with DID-aware systems without changing its canonical identifier.

Steps:

1. Publish `https://<identity>/.well-known/did.json`.
2. Resolve the DID document.
3. Compare the verification method in the DID document with the key published in DNS.
4. Verify the same signed auth response with both representations.

Expected result:

- DNS and DID document expose equivalent verification material.
- Both verification paths accept the same legitimate signature.

### Send Path Suite (SEND-01, SEND-02)

Goal: verify the v1 send path is direct-to-recipient by default and that
the `--via-home-relay` opt-in privacy proxy still functions.

These tests run as Go tests under `apps/integration/direct_send_test.go`
and use a relay-handler middleware that counts `POST /messages` and
`POST /acks` requests so they can assert routing decisions, not just
end-to-end success.

`SEND-01` spins up two relays (Alice's home relay A, Bob's home relay B),
runs a default `poweur send` from Alice, and asserts that A's POST
counter does NOT increment while B's does. The recipient's inbox decrypt
proves the message landed.

`SEND-02` repeats the scenario with `--via-home-relay`: A's POST counter
must increment by one (the client → home hop) AND B's must too (the home
→ recipient forward). End-to-end decryption still succeeds, and the
counter assertion is what proves the message took the proxied route.

### Forwarding Rule Suite (FWD-01 .. FWD-03)

Goal: pin down the at-least-one-local rule that gates both `POST /messages`
and `POST /acks`. The relay accepts a message iff at least one of its
`sender` and `recipient` is a locally hosted identity (in-memory store or
DNS A/CNAME points here); otherwise it returns `403 not_authorized`.

`FWD-01` is the both-local case: Alice on relay A, sender == recipient ==
`alice.poweur.net`. The relay stores the message and the next `inbox`
poll decrypts it.

`FWD-02` is the neither-local case: a structurally-valid encrypted
envelope (correct fields, plausible encryption metadata, dummy signature)
is POSTed to a third relay where neither Alice nor Bob is hosted. The
relay short-circuits with `403 not_authorized` BEFORE signature
verification — this is the rule that prevents an open-forwarder hazard.

`FWD-03` is the same neither-local check on the ack endpoint, asserting
that the rule is enforced uniformly across `/messages` and `/acks`.

### Delivery Ticks Suite (DLV-01 .. DLV-04)

Goal: exercise the WhatsApp-style two-tick model end-to-end through the
real CLI, including the per-identity pending journal at
`~/.poweur/pending/<identity>.jsonl`.

These tests use `poweur messages status --json [--id <id>]` to read
back the journal so they exercise the user-facing surface, not just the
on-disk file format.

`DLV-01` asserts tick 1: a successful default send records
`delivered_recipient_relay` for the outbound message id.

`DLV-02` asserts tick 2: after the recipient runs `inbox` (which decrypts
and emits a signed `delivered_client` ack to the original sender's home
relay), the sender's next `inbox` poll drains the acks array and the
journal advances to `delivered_client`.

`DLV-03` asserts the offline-recipient case: Alice's journal stays pinned
at tick 1 regardless of how many times she polls her own inbox if Bob
never decrypts.

`DLV-04` asserts forgery rejection: a structurally-valid ack envelope
with a signature produced by an unknown key is rejected with `401
unauthorized` by the home relay; the sender's journal does NOT advance
to tick 2.

### Admin Auth Suite (ADMIN-01 .. ADMIN-06)

Goal: pin down the owner-only ("admin") endpoint class. In v1 the
messaging surface is intentionally open (anyone may post a properly-
signed message or ack), but state-mutating per-identity endpoints
(`POST /identities`, `POST /identities/:identity/encryption-key`,
`DELETE /sessions/:id`) require an identity-signed admin envelope —
`issued_at`, `nonce`, `identity_signature` over a relay-known canonical
string.

For each admin endpoint the suite asserts BOTH:

- The unsigned case (no admin envelope) is rejected with
  `400 invalid_request` before any side effect.
- The forged-signature case (envelope present, signature produced by a
  different key) is rejected with `401 unauthorized`.

The positive (correctly signed) paths are exercised by every other
integration test that runs `identity create` or
`identity add-encryption-key`, so they are not duplicated here.

### Integration Suite (INT-01 .. INT-09)

All `INT-*` scenarios run as Go tests under `apps/integration/` and exercise
the real CLI, real relay HTTP server, real crypto, and a fake in-memory DNS
zone that backs both the CLI's resolver and the relay's resolver/provider.
Everything runs in a single process — no external DNS, no external network.

Shared test scaffolding:

- `apps/integration/fakedns/Zone` implements the CLI `identity.Resolver`,
  the relay `dns.Resolver`, and exposes a `dns.Provider` via `Zone.Provider()`.
  One zone instance is shared between one or more in-process relays and the
  CLI, so writes the relay performs on `POST /identities` are instantly
  visible to the next CLI DNS lookup.
- `apps/api/pkg/relay` and `apps/cli/pkg/cli` are thin exported façades over
  the internal packages so the integration module (a separate Go module with
  `replace` directives) can drive both sides in-process.
- Every relay is an `httptest.Server` whose `RelayAddress` is pinned to its
  listener address (`host:port`). Cross-relay forwarding over HTTP therefore
  reaches the right `httptest` server automatically.

Run with `go test github.com/poweur/integration/...` from the workspace
root, or `cd apps/integration && go test ./...`.

#### INT-01: Identity creation publishes DNS

Goal: prove the CLI → relay → DNS write path is closed end-to-end.

Steps:

1. Spin up a single relay backed by a fresh `Zone`.
2. Give Alice a temp `HOME` and run `poweur identity create alice`
   against the relay with the mock provider.
3. Inspect `Zone.Snapshot()`.

Expected result:

- `TXT:_poweur.alice.poweur.net` contains `poweur-pubkey=ed25519:<b64>`.
- `TXT:_poweur-enc.alice.poweur.net` contains `poweur-enckey=x25519:<b64>`.
- `HOST:alice.poweur.net` points at the relay's `host:port`.

#### INT-02: Encrypted round-trip on one relay

Goal: verify the default happy path — encrypt, sign, deliver, decrypt — with
no relay-level ambiguity.

Steps:

1. Both Alice and Bob create identities against the same relay.
2. Alice runs `poweur send bob.poweur.net "<secret>"` (default = encrypted).
3. Bob runs `poweur inbox` (text mode) on his `HOME`.

Expected result:

- Bob's output contains `alice.poweur.net`, the decrypted plaintext, and the
  🔒 lock glyph that the CLI only prints when it successfully decrypted a
  real encryption envelope.

#### INT-03: Cross-relay forwarding + session proof

Goal: prove that a message whose session was issued by relay-A still
verifies on relay-B without any shared session state.

Steps:

1. Start two relays bound to different ports.
2. Alice registers on relay-A, Bob on relay-B (each on their own temp HOME).
3. Alice sends to Bob. Her relay forwards over HTTP to Bob's relay.

Expected result:

- Relay-A local-verifies against its cached identity.
- Relay-B, with no session cache, accepts the embedded `session_proof` by
  fetching Alice's long-lived signing key from the shared DNS zone, caches
  the session, and delivers the ciphertext.
- Bob's text-mode inbox decrypts the message.

#### INT-04: Session auto-refresh on local expiry

Goal: exercise the "next send after 24h" path without waiting 24 hours.

Steps:

1. Alice and Bob create identities on the same relay.
2. Alice sends `"first message"` — this auto-registers session `S1`.
3. The test rewrites Alice's session TOML with `expires_at` in the past.
4. Alice sends `"second message"`.

Expected result:

- The CLI sees the local session as invalid, generates a new session,
  re-signs the registration with the long-lived identity key, and POSTs it.
- The on-disk session id rotates (`S2 != S1`) and `expires_at` jumps back
  into the future.
- Bob's inbox contains both messages.

Note on mobile: this is the exact moment a mobile client would prompt the
user for passkey/biometric unlock. The CLI skips the prompt because the
identity private key lives on disk.

#### INT-05: Graceful decryption failure

Goal: make sure a client that has lost its encryption private key never
prints ciphertext as if it were plaintext.

Steps:

1. Alice and Bob create identities and Alice sends an encrypted message.
2. Before Bob drains his inbox, the test removes
   `$BOB_HOME/.poweur/keys/bob.poweur.net.enc`.
3. Bob runs `poweur inbox`.

Expected result:

- Output contains `[encrypted: no local encryption key]`.
- Output does NOT contain the original plaintext (there is no way it could,
  but we assert it explicitly to guard against regressions that start
  printing raw ciphertext).

#### INT-06: Replay rejection (pending)

Goal: reject a signed message submitted twice.

Current status: skipped. The relay today performs signature verification
and rate-limiting but does not track a per-sender seen set. The test is
committed so that, as soon as dedup ships, unskipping is a one-line change.

Planned steps:

1. Alice registers a session and sends message `M`.
2. The test re-POSTs the exact bytes of `M` to the relay.

Expected result:

- First POST returns 202.
- Second POST returns 4xx with a replay-specific error code.
- Bob's inbox contains `M` exactly once.

#### INT-07: Tampered signature rejected at the edge

Goal: verify the cheap fast-path rejection when someone posts a forged
envelope claiming to be Alice.

Steps:

1. Alice and Bob create identities; Alice sends one real message so that
   the relay has her session id cached and the inbox has a known baseline.
2. The test constructs a raw JSON message that is shape-valid — correct
   `sender`, real `session_id`, valid base64 signature — but the signature
   was produced under a random Ed25519 key signing an unrelated string.
3. The test POSTs the forged JSON directly to `/messages`.

Expected result:

- Relay returns `401 unauthorized`.
- Bob's inbox, drained afterwards, contains the legit message but NOT the
  forged `"impersonation attempt"` payload.

#### INT-08: DNS tamper breaks cross-relay verification

Goal: prove that relays do not blindly trust forwarded messages; each relay
re-checks DNS for the sender's identity key.

Steps:

1. Start two relays. Alice on relay-A, Bob on relay-B.
2. An attacker overwrites `_poweur.alice.poweur.net` in the shared zone
   with a public key nobody in the system holds. Alice's encryption key and
   Alice's cached key on relay-A are untouched.
3. Alice sends to Bob.

Expected result:

- Relay-A verifies with its cached key (success) and forwards to relay-B.
- Relay-B has no cached key for Alice; it resolves DNS, finds the forged
  key, and fails to verify the session proof.
- Relay-B returns non-`202`, relay-A bubbles that up as `502 forward_failed`,
  the `send` CLI exits non-zero.
- Bob's inbox does NOT contain the tampered message.

#### INT-IDSIGN-01 / INT-IDSIGN-02 / INT-IDSIGN-03: Identity-signed send

Goal: exercise the `poweur send --sign-with=identity` opt-out path — the
sender skips session registration and signs the message directly with the
long-lived identity Ed25519 key. Encryption is unchanged; only the
verifying key differs on the relay side.

Steps (same-relay, INT-IDSIGN-01):

1. Alice and Bob create identities on the same relay.
2. Alice runs `poweur send --sign-with=identity bob.poweur.net "<secret>"`.
3. Bob runs `poweur inbox`.

Expected result:

- Relay accepts the envelope: `session_id` is empty, so the relay resolves
  Alice's verifying key from her in-memory identity cache (or DNS TXT
  record).
- Bob's inbox shows the decrypted plaintext with the 🔒 glyph.
- Alice's local session file at `$ALICE_HOME/.poweur/sessions/alice.poweur.net.toml`
  does NOT exist afterwards (the whole point of the flag is to bypass it).

Steps (cross-relay, INT-IDSIGN-02):

1. Start two relays. Alice on relay-A, Bob on relay-B.
2. Alice runs `send --sign-with=identity bob.example.org "<secret>"`.

Expected result:

- Relay-A verifies with its cached identity key and forwards to relay-B.
- Relay-B has no knowledge of Alice, resolves `_poweur.alice.poweur.net`
  via DNS, and accepts the signature.
- Bob's inbox contains the decrypted plaintext.

Steps (forged signature, INT-IDSIGN-03):

1. Alice and Bob create identities. The test constructs a JSON envelope
   with valid shape, `session_id` omitted, and a signature produced by a
   random Ed25519 key over an unrelated string.
2. The test POSTs the forged JSON directly to `/messages`.

Expected result:

- Relay returns `401 unauthorized`.
- Bob's inbox contains no trace of the forged payload.

#### INT-09: Full two-way conversation

Goal: a single, realistic story that exercises everything at once.

Steps:

1. Alice and Bob each run `identity create` in their own `HOME`, generating
   Ed25519 signing keys, X25519 encryption keys, and publishing both TXT
   records + the relay host record into the zone.
2. Alice sends `"hey bob, dinner at 7?"`. Her first send triggers the
   session-registration flow; a short-lived Ed25519 session key is
   generated, signed by her long-lived key, registered with the relay, and
   persisted locally.
3. Bob runs `inbox`, decrypts, and sees Alice's message.
4. Bob sends `"sure alice, see you then"` — his own first send, which
   triggers his session registration.
5. Alice runs `inbox`, decrypts, and sees Bob's reply.

Expected result:

- Each inbox output contains exactly one decrypted message with the 🔒
  marker.
- Each user sees only the peer's message, never their own outbound copy.
- Plaintext matches what the sender typed.

## Recommended CI Split

Run these groups separately:

- `docs`: docs build and link checks
- `protocol-unit`: canonical signing, verification, envelope parsing
- `relay-e2e`: local relay plus local DNS scenarios (`apps/api/internal/relay`)
- `cli-e2e`: CLI plus mock relay (`apps/cli/internal/cli`)
- `integration-e2e`: full in-process CLI + relay(s) + fake DNS (`apps/integration`, `INT-*`)
- `provider-integration`: Cloudflare and Hetzner staging runs
- `mobile-manual`: iOS and Android checklist-driven manual validation

## Exit Criteria

The system is ready for serious external trials when:

- all automated relay and CLI scenarios pass consistently
- provider-backed integration succeeds for at least one supported DNS provider
- both mobile platforms complete identity creation and auth approval flows manually
- the third-party sign-in flow is validated on both QR-based and deep-link based paths
