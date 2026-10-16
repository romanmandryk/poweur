# EPIC-030 — Off-grid & peer-to-peer resilience (DRAFT)

- **Status:** **draft**, 2026-10-16 (A6 nearby messaging reworked the same day). Not ready to implement. Each area below says how settled it
  is, and the *Needs more thought* list must be worked through (mostly by the spikes in
  T0) before tasks get acceptance criteria.
- **Priority:** P3 (after EPIC-029 has shipped and shown what real outages look like)
- **Depends on:** [EPIC-029](EPIC-029-relay-outage-resilience.md) (last-known-good store, relay
  sets, mailboxes, self-contained policy at a relay), EPIC-011 (device registry, rotation,
  E11-T13 device removal), EPIC-020 (storage v2), EPIC-024/025 (groups, real-time), EPIC-019
  (mobile shell), EPIC-022 (OAuth bridge)
- **Touches (expected):** `packages/identity`, `packages/client-ts`, `apps/api`, `apps/cli`,
  `apps/web`, `apps/mobile`, `apps/integration`, `apps/docs`, `conventions`; possibly a new
  native transport module

## Where EPIC-029 stops

EPIC-029 keeps contacts talking when **one** relay or **one** DNS zone fails while the internet
is up, using relays the owner set up in advance. It does not cover:

| Gap | Example |
|---|---|
| G1 New names while DNS is down | Alice wants to message `carol.example.org`, whom she has never resolved |
| G2 Proving a stranger's name offline | A mailbox can't tell whether a stranger really is `carol.example.org` |
| G3 No pre-arranged backup | Alice never set up a mailbox, or both her relays are down |
| G4 No internet at all | Friends in a mall, or a building or town cut off; only local Wi-Fi, a hotspot or Bluetooth works |
| G5 Bulk data without a relay | Sharing a 2 GB folder with a contact on the same Wi-Fi goes through a relay today |
| G6 Drive and shares | Opening a shared folder needs the owner's relay |
| G7 Rotation and revocation while partitioned | A stolen laptop can still sign as you; contacts can't learn about the rotation |
| G8 Sign-in | The OAuth bridge or the user's relay is down |

## Guiding principles

1. **The name stays the user-facing ID.** Everything here is a fallback underneath it. When
   it's in use, the UI says so (EPIC-029 T6 states plus new ones), never silently.
2. **Signed data, untrusted carriers.** Every new statement is signed by a key the verifier
   already holds or can chain to. Any host, peer, DHT or Nostr relay may carry it. Carriers can
   withhold or replay; signatures and `seq` numbers stop forgery and rollback.
3. **Contacts get the strong path; strangers get the gated path.** Direct, low-friction
   delivery is for pinned contacts. Strangers always go through something that enforces
   policy, rate limits and PoW.
4. **Degraded trust is shown, not hidden.** "Name unverified", "via a relay you don't usually
   use", "location visible to this contact".
5. **No new mandatory infrastructure.** Each piece is optional, and the protocol still works
   with only EPIC-029.

## Areas

Each area has a draft design, its maturity (**sketch** = direction only, **shaped** = most
choices made, **ready** = could be specified now) and the questions still open.

### A1 — Name attestations (G1, G2) — *shaped*

A signed, cacheable statement that a name maps to a key, issued by whoever is authoritative
for the name:

```
poweur-name-attestation-v1
name: carol.poweur.net
public_key: ed25519:…
issuer: poweur.net                 # the hosted parent; or carol.example.org itself
issuer_key: ed25519:…
issued_at / expires_at             # e.g. 30 days, refreshed weekly
signature
```

- **Hosted IDs:** the parent operator signs (key published at
  `https://poweur.net/.well-known/poweur/issuer.json` and pinned by clients, like a tiny CA).
- **Own-domain IDs:** self-issued attestations prove nothing extra offline. Either accept that
  own-domain names are only verifiable online, or allow cross-attestation (A1b).
- Clients fetch their own attestation while online and **attach it to contact requests and
  first messages**, so the receiver can check the name with no lookup.
- Relays and mailboxes accept "name proven by attestation" as an alternative to fresh
  resolution for strangers (EPIC-029 D5 step 2).

Needs more thought:
- Issuer-key compromise and rotation, and how clients pin issuer keys. One issuer per hosted
  parent? Transparency log?
- A1b: should contacts or third parties be able to attest ("Bob vouches that this is Carol")?
  That turns into a web of trust. Probably as an *introduction* shown to the user, never as a
  machine-trusted proof.
- How does this interact with `moved_to` and name policy (EPIC-018) when a name is released and
  re-registered? Short expiries help; is 30 days too long?

### A2 — Self-contained sender proof in envelopes (G2, G3) — *shaped*

An optional envelope field carrying what any relay needs to verify the sender without
resolving:

```
sender_proof: {
  document: <sender's signed id.json, with relay_set>,
  attestation: <A1, optional>,
  device_cert: <A5, optional>
}
```

- Not part of the canonical signing string. It's evidence, and the envelope signature
  already covers the identity. Size limit so it can't bloat envelopes (≤ 8 KB).
- Generalises EPIC-029's "owner's pins" step: any relay can verify a contact's message using
  the recipient's pins, and a stranger's using an attestation.

Needs more thought:
- Is attaching it always (bigger envelopes, more metadata on the wire) or only on fallback
  paths better? Probably on first contact and on any non-home delivery.
- Replay of an old `document` with an old key that the sender has since rotated away from
  because it leaked: requires the verifier to know about the rotation (A7).

### A3 — Dynamic relay delegation ("takeover") (G3, G4) — *shaped*

EPIC-029 mailboxes are registered in advance and listed in the published relay set. A
**delegation** is the ad-hoc version: a short-lived, signed statement that relay R may hold mail
for the ID right now.

```
poweur-relay-delegation-v1
identity: alice.poweur.net
relay: <url or relay node key>
seq, not_before, expires_at         # hours to days
signature (identity key)
```

- Delivered to R along with the EPIC-029 registration payload (policy, pins, blocked list).
  R behaves exactly like a mailbox until it expires.
- Spread to contacts through A4 (gossip, DHT, Nostr, mDNS), so senders who can't reach any
  relay in the published set learn where to deliver.
- Effectively a relay set with an expiry. **Maybe EPIC-029's relay set just gains an optional
  `expires_at` per entry**, and delegation needs no separate statement. Decide in T0.

Needs more thought:
- Who may host? Any relay with `MAILBOX_HOSTING=open`? Island relays run by a neighbour (A6)?
  How does the UI ask "use this relay?" without training people to click yes?
- Abuse: a malicious relay asking clients to delegate to it (phishing a local relay). Delegation
  only ever happens on the owner's explicit action or to relays already in their set.
- Merging back: when the home returns, the owner polls the delegated relay until it expires.
  Same as EPIC-029 D4. Probably fine.

### A4 — Discovery & distribution layer (G1, G3, G7) — *sketch*

A pluggable list of **sources** that can return signed statements (documents, relay sets,
delegations, attestations, rotations) for a key or a name. Each source is untrusted; the newest
valid signature wins. Order:

1. local store (EPIC-029 T1)
2. own devices (sync)
3. contacts (gossip)
4. nearby devices (mDNS, Bluetooth, A6)
5. Mainline DHT via pkarr
6. Nostr relays
7. web / DNS (today's resolver)

Draft per source:

- **Own devices:** every device of an ID shares its store through the sync journal (EPIC-004 /
  PCP-0005) or a new `sys.identity.*` system message. A cheap first step.
- **Gossip through contacts:** a `sys.identity.update` message type carrying a signed
  statement about *the sender itself* (its new relay set, delegation or rotation), sent to
  accepted contacts when it changes. Never about third parties, so no relaying of other
  people's data and no graph leak.
- **pkarr / Mainline DHT:** the BitTorrent DHT (about 10 million nodes, no accounts) stores
  small signed mutable records keyed by an Ed25519 key (BEP-44, ≤ 1000 bytes, `seq`, expires in
  hours, so someone must republish). pkarr puts signed DNS-style records there. We'd publish a
  **pointer**, not the whole document: relay URLs, a hash of the current `id.json`, the
  delegation if any.
  - Lookup is **by key, not by name**, so it serves contacts (whose keys we pin), not G1.
  - Republishing: the owner's always-on devices, the home relay and mailboxes can all
    republish the same signed record.
  - Bootstrapping: the default bootstrap nodes are found by hostname, so cache peer IPs.
- **Nostr relays:** thousands of independent WebSocket servers that store and forward signed
  events. A replaceable event could hold the full `id.json` plus relay set, and could serve as a
  dumb encrypted mailbox of last resort.
  - Nostr uses secp256k1 Schnorr keys, so a Poweur ID would need a Nostr key certified by its
    identity key.
  - It's browser-friendly (`wss://`). But relays drop data at will, rate-limit, or charge.

Needs more thought:
- **Privacy of publishing.** Relay URLs are fine. Device IPs or node addresses in a public DHT
  reveal location: publish them encrypted to contacts, or not at all. Never publish contact
  lists.
- **Is the DHT worth its weight?** Its unique value is "a contact's key → where they are now,
  with no infrastructure of ours". Gossip and Nostr cover much of that. A spike (T0-S2) should
  measure pkarr lookup latency and reliability from Go and the browser (through HTTP gateways,
  which need the internet anyway).
- Nostr key certification format, and whether Nostr adds enough over our own relays to justify
  a second key type.
- Rate-limiting and caching rules for each source so discovery isn't an amplification vector.

### A5 — Device transport keys & device certificates (G4, G5, G7) — *shaped*

Peer-to-peer needs per-device addressing. Today every device holds the same identity key and
encryption key (EPIC-011 notes), so a device can't be revoked on its own.

- Each device generates a **transport key** (Ed25519). For Iroh this *is* the node id; for
  libp2p it is the peer id.
- The identity key signs a **device certificate**:
  `poweur-device-cert-v1 / identity / device_id (registry) / transport_key / issued_at /
  expires_at`, short-lived (7–30 days), renewed by any unlocked device.
- Contacts accept direct connections only from transport keys with a valid cert chaining to
  the pinned identity key.
- The device registry (E11-T11) lists device ids and certs; revocation = stop renewing, plus an
  explicit revocation list carried by gossip (A7).

Needs more thought:
- Is the session key (24h, identity-authorized) already this? Session proofs are relay-scoped
  today (`session_id`). A device cert is the same shape with no relay id: maybe one primitive
  with an optional `relay` field. Decide before adding a second.
- Does a device cert let *signing* move to per-device keys too (envelopes signed by the
  device, cert attached)? That would make E11-T13 removal real for new messages. It also
  changes what relays verify. Large, and worth its own decision.

### A6 — Nearby: zero-config direct messaging without the internet (G4) — *shaped*

**Scenario.** Bob, Carl and Dave are in a shopping mall. The internet is down (or poweur.net is),
but their phones can still reach each other locally. They are already contacts. They should be
able to message each other with no setup on the spot, and nobody else in the mall should learn
who they are or that they know each other.

#### Not a `.local` identity

`.local` is mDNS's unowned namespace: any device can announce `alice.local`, and collisions just
rename one device (`alice-2.local`). A `.local` name proves nothing. Reusing Alice's keys under it
adds no security (the key already identifies her) and removes privacy (everyone nearby learns
`alice.poweur.net` is here). So:

- **On a local link the key is the identity**, and the receiver already has it pinned.
- **The displayed name comes from the receiver's contact list**, never from the broadcast.
- **The broadcast says nothing to people who aren't contacts.** The instance name is a random
  session label (`lonely-red-wolf`), new each session.

#### Recognition tags (directional, rotating)

Every pair of contacts already shares a secret with no setup. Each holds the other's X25519
encryption key from `id.json`, so both can compute the same ECDH value:

```
shared   = X25519(own_enc_priv, peer_enc_pub)
K_pair   = HKDF-SHA256(shared, salt = "poweur-nearby-v1",
                       info = min(id_a, id_b) || "\n" || max(id_a, id_b))
slot     = floor(unix_time / 900)                      # 15-minute slots
tag(A→B) = HMAC-SHA256(K_pair, "tag\n" || id_A || "\n" || slot)[0:8]
psk      = HKDF-SHA256(K_pair, info = "psk")
```

- Alice advertises `tag(Alice→c)` for each nearby-enabled contact `c`. Bob checks whether
  `tag(Alice→Bob)` is present for the current slot (±1 for clock skew).
- **Directional:** Bob advertises `tag(Bob→Alice)`, a different value. Two devices never
  broadcast the same tag, so an observer can't spot that two people are contacts.
- **Rotating:** a new slot every 15 minutes, so a device can't be followed across the mall or
  across days.
- **Fixed size:** tags go into a fixed-size Bloom filter (proposal: 256 bytes, enough for
  about 200 contacts at a 1% false-positive rate), padded the same for everyone, so its size
  doesn't reveal contact count. Users with more contacts pick which ones are *nearby-enabled*
  (default: the most recent 200 messaged).
- All of an ID's devices share the encryption key, so all of them produce the same tags. That's
  fine: any of them can answer.

Announcement over mDNS / DNS-SD:

```
instance: lonely-red-wolf            (random per session)
service:  _poweur-nearby._udp.local   port <p>
TXT:      v=1  k=<ephemeral transport public key>  t=<Bloom filter, base64url>
```

#### Handshake

A tag match alone isn't proof: an attacker can copy Alice's broadcast within the slot. So the
connection proves knowledge of `K_pair` interactively, then binds identities:

1. Noise `XXpsk3` (or the QUIC/TLS equivalent of the chosen stack, A8) with the ephemeral
   transport keys and `psk` for the pair. Only someone holding one of the two private encryption
   keys can complete it.
2. Inside the channel, each side sends `sign(identity_key, "poweur-nearby-bind-v1\n" ||
   handshake_hash)` (or a device-cert signature, A5). The other checks it against the pinned
   key.
3. Then the two exchange **unchanged end-to-end encrypted envelopes**, the same objects a relay
   would carry, plus acks. The journal records `delivered_nearby` (new state, see A8).

Nobody reveals an identity before the other side has proven it holds the pair secret.

#### Transports: mDNS is one of several

Public and mall Wi-Fi usually has **client isolation** and blocks multicast, so mDNS often fails
there even when the uplink is up. Discovery and links are therefore plug-ins under one interface
(tags and the handshake are the same on all of them):

| Link | Between | Notes |
|---|---|---|
| Same Wi-Fi + mDNS | any native | homes, offices, friendly networks; rarely malls |
| One phone's hotspot | any native | Bob turns on a hotspot, Carl and Dave join, mDNS then works. The simplest reliable fallback; the app can suggest it |
| Bluetooth LE | iOS ↔ Android | the only cross-platform radio; slow but fine for text; short range. Legacy advertising holds 31 bytes, so the Bloom filter is read over GATT after a short advertised service id |
| Apple Multipeer (AWDL) | iOS / macOS | fast, no shared network needed |
| Wi-Fi Aware / Wi-Fi Direct; Google Nearby Connections | Android | fast; Nearby needs Play Services |

Prior art to study before specifying: Briar (Bluetooth/LAN contacts-only sync), Berty (libp2p,
mDNS, BLE, rotating ids), Bridgefy (BLE mesh, and its published security issues).

#### Bob, Carl and Dave: what has to be true

Done beforehand, almost all automatically:

1. They are contacts with pinned keys (exists today).
2. Cached identity documents (EPIC-029 T1), so encryption keys are at hand.
3. **The native app**: browsers can't do mDNS, BLE or raw sockets, so the PWA can't take part.
4. **OS permissions, granted once** at setup under "Messaging nearby when offline":
   - iOS: Local Network (with `NSBonjourServices` declared) and Bluetooth;
   - Android: Nearby Wi-Fi Devices, Bluetooth, a multicast lock.

   This is the only real preparation step.

In the mall:

1. Each app sees no relay is reachable (EPIC-029 `degraded`) and starts nearby discovery. A
   setting can also run it whenever the app is open.
2. Bob's app matches Carl's and Dave's tags and connects; the UI shows *Carl and Dave are
   nearby; messages to them go directly*.
3. Messages and acks go device to device. Messages to anyone not nearby wait in the outbox.
4. Group chats: each sender sends to every nearby member; the rest get it through relays later.
5. **Courier (opt-in, mutual contacts only):** Carl carries Bob's encrypted message for Dave if
   Dave has gone out of range. Carl can't read it; limits on size and time held.
6. When the internet returns, the outbox skips entries already acknowledged nearby; anything
   resent is dropped by message id.

Limits to state in the UI:

- **iOS stops local discovery in the background, and there is no push without a relay.** In
  practice, friends see nearby messages when they open the app.
- Range is one network, or tens of metres over Bluetooth.
- A nearby contact can see your local IP address.

#### Strangers: throwaway personas and in-person QR

- **Throwaway persona.** To talk to strangers nearby (an AirDrop-style *everyone for 10
  minutes*, or a disaster), the app makes a **fresh key** not linked to the main ID, shown as
  `lonely-red-wolf`. It is deleted after the session or day. The user can later link it with a
  signed note ("lonely-red-wolf was me, alice.poweur.net"). Strangers get policy and rate limits
  on the device: requests queue, no attachments.
- **In-person QR contact exchange.** Scanning each other's QR codes carries the signed identity
  document and key. That's stronger than a network lookup for the key; the *name* is checked
  when back online, and the contact is marked verified or flagged on mismatch.

#### Optional: island relay

For larger groups, or longer courier storage, any CLI or desktop install can run
`poweur relay --island`: a mailbox-mode relay (EPIC-029 `RELAY_MODE=mailbox`) found through the
same nearby discovery. People delegate to it (A3) explicitly. Off-island mail queues there and is
forwarded when the uplink returns. Multi-subnet islands (campus, municipal mesh) can't use mDNS,
so they need an island relay at a known address (on paper or QR), cached peer IPs, or a private
DHT among island nodes. Browsers can't join: an HTTPS page can't call `http://192.168.x.x`
(mixed content, Private Network Access), and a LAN relay can't get a publicly trusted
certificate without DNS.

Needs more thought:

- **Reusing the X25519 encryption key for tag derivation.** Domain-separated by HKDF, but it
  gives that key a second job. Should nearby use a dedicated static key published in `id.json`
  instead? A leaked encryption key would also let the holder recognise the victim's tags.
- **Slot length, Bloom filter size and the contact cap.** 15 minutes and 256 bytes are guesses;
  measure battery cost and false-positive rates in T0-S4.
- **BLE details**: advertising rotation versus the OS's own MAC randomisation; GATT read rate
  limits; iOS background advertising restrictions (an app in the background can't advertise its
  service data freely).
- **Abuse of the stranger path**: nearby spam from throwaway personas. Is PoW on the device
  enough, or should persona-to-persona messaging be off by default?
- **Courier limits**: size, hold time, how many hops (one, probably), and whether the courier's
  storage counts against anything.
- **TLS for island relays without DNS**: a self-signed certificate pinned via the relay's key
  (native only), a certificate pre-issued while online for a name under a Poweur-controlled
  domain, or plain HTTP with only signed envelopes (tokens and metadata in clear).
- **How to ask a user to trust an island relay** ("Use *Dana's Pi*?") without training a
  reflexive yes.

### A7 — Rotation & revocation under partition (G7) — *sketch*

- Rotation statements (`previous_keys`) and device revocations travel by gossip (A4) to
  contacts and by sync to own devices, not only by `id.json` fetch.
- **Short device-cert lifetimes (A5) are the real backstop**: a revoked device stops being
  trusted when its cert expires, even if no revocation reached a peer.
- Identity-key compromise while partitioned can't be fixed by the protocol alone. Contacts
  keep trusting the pin until they learn of the rotation. Document it as a limit.

Needs more thought:
- Conflict: two devices rotate concurrently while partitioned. Need a rule (`seq` + the
  recovery-master role from E11-T2 wins?).
- How long should a peer trust a pin it can't refresh? Expire trust after N days of no
  confirmation for *new* sessions, while still showing old messages?

### A8 — Peer-to-peer transport (G4, G5) — *sketch; needs the T0 spike*

A transport that carries the **same envelope** directly between devices, plus bulk data.

- **Contacts only:** the handshake checks the peer's transport key against device certs (A5)
  chaining to a pinned contact. Unknown peers are refused at connect time.
- **Uses:** messages when both sides are online; large file transfer between contacts; LAN
  device sync; low-latency real-time collaboration (EPIC-025).
- **Not for:** strangers, offline recipients (relays), or users who don't want their IP seen by
  contacts (setting: *direct connections: contacts / never*; via-relay-only transport mode).
- **Mobile:** iOS doesn't keep background sockets, so phones are foreground-only peers and can't
  be mailboxes. Push still needs a relay.
- **Browser:** WebRTC data channels only, with signaling over a relay. That gives P2P bulk
  transfer between browsers while a relay is up, not when it's down.

Library candidates, to evaluate in T0-S1:

| | Iroh | go-libp2p / js-libp2p |
|---|---|---|
| Language fit | Rust core; Swift/Kotlin/Python via FFI; WASM in browser (relay-only) | Native Go (relay, CLI) and JS (web) |
| Identity fit | Node id = Ed25519 key, like ours | Peer id from Ed25519 key, also fine |
| Connectivity | QUIC, hole punching, encrypted relay fallback (can self-host `iroh-relay`) | QUIC/TCP/WebRTC/WebTransport, DCUtR hole punching, circuit relay v2 |
| Discovery | DNS (n0-run by default), pkarr/Mainline DHT, mDNS | Kademlia DHT, mDNS, rendezvous |
| Bulk data | iroh-blobs: BLAKE3 verified streaming | bitswap / custom protocols |
| Caveats | **Defaults depend on n0's own DNS and relays; must self-host.** FFI and mobile build size; check Go binding status | Larger API surface; heavier; browser transport quirks |

Needs more thought:
- One stack everywhere, or per platform (libp2p in Go and web, Iroh in mobile)? Interop is
  the deciding factor: two stacks means two transports to test against each other.
- Does P2P change the delivery-ack semantics (EPIC-009)? "Delivered to device" happens
  directly; tick 1 (relay accepted) doesn't exist. Need new journal states.
- Ordering and dedup when the same message goes by P2P and later lands in a mailbox. Dedup by
  id is there; consume-from-relay must not resurrect it.

### A9 — Drive & shares without the owner's relay (G6) — *sketch*

- If storage v2 chunks are addressed by a hash of their ciphertext, they can be fetched from any
  source (relay, own device, the sharer's device, an island relay) and verified locally. Check
  EPIC-020's chunk-id definition; if ids aren't content hashes, that's a v3 question, not a
  patch.
- Share grants (PCP-0003) must verify offline: signed by the owner, self-contained, with
  `expires_at`. Revocation reaches peers only by gossip or expiry.
- Writes while partitioned need conflict rules. EPIC-020's replace/append commits and
  EPIC-025's CRDT work decide this; this epic only adds transport.

Needs more thought: all of it. Possibly a separate epic once A8 exists.

### A10 — Sign-in resilience (G8) — *sketch*

- Direct "Sign in with Poweur" works when the relying party has a cached key and the challenge
  reaches the user's device. Is a cached document (EPIC-029 T1) acceptable for a relying party?
  Probably only with a short `max_stale` and an attestation (A1).
- The OAuth bridge is a single service. Multi-region or a second instance is an ops question,
  not a protocol one. Track in EPIC-022.

### A11 — Groups under partition — *sketch*

Group ID fan-out runs on the group's home relay (EPIC-024). Under partition: should members
fan out directly to each other, using the member list and group key they hold? This interacts
with epochs and membership changes. Defer to EPIC-024/025 owners; list here so it isn't
forgotten.

## Draft tasks

Not ready for implementation; each needs acceptance criteria once T0 settles the open questions.

- **E30-T0 Spikes & decisions**
  - [ ] S1 P2P stack: Iroh vs libp2p. Build a minimal contact-to-contact envelope exchange in
        CLI (Go) and one mobile platform; measure binary size, NAT traversal success on common
        networks, battery, browser story. Deliverable: a decision record.
  - [ ] S2 pkarr/DHT: publish and look up a signed pointer from Go and through an HTTP gateway
        from the browser; measure latency and success; decide whether A4's DHT source is worth
        building.
  - [ ] S3 Nostr: same for a replaceable event; decide the key-certification format or drop it.
  - [ ] S4 Nearby transports (A6): mDNS on real public Wi-Fi, a phone hotspot, BLE between iOS and
        Android, Multipeer and Wi-Fi Aware; measure discovery time, battery and Bloom-filter
        false positives; check iOS background limits; try the island-relay TLS options. Decide the
        first transports and the tag parameters.
  - [ ] S5 Device certs vs session proofs (A5): one primitive or two; per-device signing yes/no.
  - [ ] S6 Threat-model workshop covering A1–A8: phishing local relays, IP leakage, gossip
        amplification, attestation issuer compromise.
- **E30-T1 Name attestations (A1)**: issuer key publishing, statement, vectors, relay and client
  acceptance rules, refresh, UI for "name proven offline / unverified".
- **E30-T2 Sender proof in envelopes (A2)**: field, size limits, relay verification order
  update, vectors.
- **E30-T3 Relay delegations (A3)**: or `expires_at` on relay-set entries, per T0.
- **E30-T4 Gossip (A4)**: `sys.identity.update` type (PCP update), own-device sync of the
  document store, contact fan-out rules.
- **E30-T5 Discovery sources (A4)**: pluggable source interface in Go and TS; DHT and Nostr
  sources if T0 S2/S3 say yes.
- **E30-T6 Device certificates (A5)**: statement, registry integration, renewal, revocation
  list.
- **E30-T7 P2P transport (A8)**: per the T0-S1 decision; contacts-only handshake, envelope
  exchange, bulk transfer, privacy setting, journal states.
- **E30-T8 Nearby direct messaging (A6)**: recognition-tag spec and vectors (Go + TS), the
  pair handshake, the nearby transport interface with mDNS and BLE first, `delivered_nearby`
  journal state, permissions onboarding in the mobile app, CLI support over LAN.
- **E30-T8b Throwaway personas & in-person QR exchange (A6)**.
- **E30-T8c Courier and island relay (A6)**: opt-in courier between mutual contacts;
  `poweur relay --island` with delegation and store-carry-forward.
- **E30-T9 Rotation and revocation by gossip (A7)**.
- **E30-T10 Drive over any source (A9)**: after checking EPIC-020 chunk addressing.
- **E30-T11 Integration scenarios**: real relays plus simulated partitions (fake DNS zones off,
  relays off, a LAN-only network namespace) for every area that ships.
- **E30-T12 Docs & threat model**.

## Needs more thought (summary)

1. **Per-device signing (A5)**: the biggest structural decision; it touches every verifier.
2. **One P2P stack or two (A8)**, and whether Go-native libp2p beats Iroh's better fit plus FFI cost.
3. **Is the Mainline DHT worth it**, given gossip and Nostr (A4, T0-S2)?
4. **Nearby (A6)**: whether to reuse the encryption key for tags, slot and filter parameters,
   which transports first (mall Wi-Fi rarely allows mDNS, so BLE matters), and TLS for island
   relays.
5. **Attestation issuer trust (A1)**: pinning, rotation, compromise, transparency.
6. **How to ask users to trust a local relay** without training reflexive "yes" (A3, A6).
7. **IP and location privacy** for direct connections and anything published to a DHT
   (A4, A8).
8. **New delivery-ack states** for P2P (A8), and dedup with mailboxes.
9. **Concurrent rotation while partitioned** (A7).
10. **Drive chunk addressing (A9)**: whether EPIC-020 already allows fetching from any source.
11. **Groups under partition (A11)**: needs the EPIC-024/025 owners.
12. **Scope check:** which scenarios matter enough to build: censorship, disasters, flaky
    relays, LAN speed. Prioritise by real demand once EPIC-029 telemetry shows outage patterns.
