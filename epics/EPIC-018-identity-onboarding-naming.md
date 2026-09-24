# EPIC-018 — Hosted identity onboarding: launcher, name policy & credential scope

- **Status:** complete — T1–T5 done
- **Priority:** P1 (the front door for every new user; blocks any public launch of a hosted domain)
- **Depends on:** EPIC-002 (hosted registration), EPIC-001 (identity documents), EPIC-014 (registration PoW gate)
- **Unlocks:** [EPIC-019](EPIC-019-mobile-app-capacitor.md) (mobile shell reuses the same onboarding), [EPIC-015](EPIC-015-web-app-ux.md) first-run flow, public availability of `*.poweur.net`
- **Plans note:** the "no paid premium names" non-goal stands under EPIC-026. Monetize custom domains instead via an optional registrar referral that auto-configures `.well-known`.

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E18-T1 Configurable name policy | **done** | `packages/identity/namepolicy.go` with typed reasons; ASCII-LDH enforcement closes the homoglyph bypass; `NAME_*` config wired into registration |
| E18-T2 Availability & policy endpoint | **done** | `GET /hosted/availability` returning a verdict + the policy; policy evaluated before registration so a rejection cannot leak whether a reserved name is taken; cost-weighted per-IP limit |
| E18-T3 Launcher host & no-identity mode | **done** | `LAUNCHER_HOST` config + `/` redirect; live availability as the user types; fragment hand-off to the identity's own origin |
| E18-T4 Credential scope (rpId) model | **done** | `registrableDomain()` in `@poweur/client`; scope stored on the identity record and reused for every assertion; pre-E18 records keep the host they were minted with |
| E18-T5 Docs | **done** | operator policy table in `relay/configuration.md`; `web/claim-your-id.md` for the user-facing flow |

## Goal

Make claiming a hosted identity on a relay's domain a safe, self-serve, operator-tunable
flow: a visitor with no Poweur ID reaches a **launcher host**, types a handle, sees live
whether it is available and why not, and completes registration with a credential that
still works once they land on their identity's own origin.

Two things stand between the current code and that flow:

1. **There is no entry point for a user with no identity.** The SPA already registers
   identities (`apps/web/js/app.js` `new-id`), but it is served per-identity at
   `https://<identity>/app/` — a visitor who does not yet have an identity has nowhere to
   start.
2. **Handle rules are hardcoded, minimal, and unsound.** They cannot be tuned per
   deployment, and they are bypassable (see below).

## Background (current code)

- Registration validates via `idpkg.ValidateHostedHandle` at
  [`apps/api/internal/relay/server.go:268`](../apps/api/internal/relay/server.go), backed by
  [`packages/identity/names.go`](../packages/identity/names.go): `MinLabelLen = 3`,
  `MaxLabelLen = 63`, and a fixed 24-entry `ReservedLabels` map. None of it is configurable.
- **Bug — homoglyph bypass.** `validateLabel` accepts any `unicode.IsLetter(r)`, so non-ASCII
  letters pass. Verified against the current package:

  ```
  "alice.poweur.net"  ->  <nil>
  "аdmin.poweur.net"  ->  <nil>     ← Cyrillic U+0430 'а'; bypasses ReservedLabels["admin"]
  "www.poweur.net"    ->  label "www" is reserved
  ```

  This is an impersonation vector *and* a correctness bug: a raw UTF-8 label is not a valid
  DNS label and is not covered by the `*.poweur.net` wildcard certificate
  (`requirements.md`, wildcard certificate strategy).
- There is **no availability check**. The web app's only client-side rule is
  `handle.length < 3`; a taken handle is discovered by a failed `POST /identities` after the
  user has already created a passkey.
- Passkeys are created with `rpId = window.location.hostname`
  ([`apps/web/js/passkey.js:30`](../apps/web/js/passkey.js)). The passkey is **not** the
  identity key — it supplies a PRF secret that wraps the Ed25519/X25519 keys
  (`wrapKeysAES`, [`apps/web/js/vault.js`](../apps/web/js/vault.js); the stored record
  carries `kdf: "prf" | "native"`). So credential scope governs *which lock opens on which
  origin*, not where the identity can live.
- `HOSTED_DOMAINS` already exists in [`apps/api/internal/config`](../apps/api/internal/config/config.go);
  this epic extends the same env-var pattern.

## Design direction

### Name policy is operator configuration, not a constant

A `NamePolicy` value on `config.Config`, loaded from env like every other relay setting, and
threaded into `packages/identity` as an explicit argument (the package keeps a
`DefaultHostedPolicy()` so existing callers and self-hosters get sane behaviour without
configuring anything).

| Field | Env var | Default | Rationale |
|-------|---------|---------|-----------|
| `MinLen` | `NAME_MIN_LEN` | `6` | anti-squatting: short handles are the scarce resource |
| `MaxLen` | `NAME_MAX_LEN` | `24` | fits UI, well under the 63-byte DNS label limit |
| `AllowHyphen` | `NAME_ALLOW_HYPHEN` | `true` | not leading/trailing, and no `--` run |
| `AllowDigits` | `NAME_ALLOW_DIGITS` | `true` | |
| `Reserved` | `NAME_RESERVED` (CSV, **adds** to built-ins) | see below | operator-specific names |
| `Blocked` | `NAME_BLOCKED_FILE` (path, one term per line) | empty | profanity / abuse list, deployment-local |
| `BlockMode` | `NAME_BLOCK_MODE` | `substring` | `substring` \| `exact` |

Hard rules that are **not** configurable, because they are correctness rather than policy:

- **ASCII LDH only** (`a–z`, `0–9`, `-`). Explicitly reject every non-ASCII rune — this
  closes the homoglyph bypass. No IDN/punycode support in v1.
- **Reject a leading `xn--`**, so a handle cannot masquerade as an encoded IDN label.
- Normalize to lowercase before every check, so `ADMIN` and `Admin` hit the reserved list.
- Reserved/blocked matching runs on the **normalized** label, after ASCII enforcement.

Built-in reserved labels extend today's set toward names the operator will plausibly want.
Grouped by why they are held back:

- *Infra / protocol* (existing): `www` `admin` `relay` `mail` `ftp` `api` `app` `static`
  `cdn` `ns` `ns1` `ns2` `mx` `smtp` `imap` `pop` `root` `localhost` `poweur` `well-known`
  `dav` `sync`
- *Product surfaces we may want to run*: `id` `ids` `launcher` `get` `join` `signup`
  `signin` `login` `auth` `account` `accounts` `console` `dashboard` `portal` `home`
- *Data / ops*: `data` `files` `file` `storage` `analytics` `metrics` `status` `health`
  `logs` `backup` `db` `search` `index` `assets` `media` `img` `images`
- *Comms / org*: `support` `help` `docs` `blog` `news` `about` `contact` `legal` `privacy`
  `terms` `security` `abuse` `postmaster` `hostmaster` `webmaster` `noreply` `no-reply`
- *Money / trust*: `pay` `payments` `billing` `wallet` `invoice` `verify` `verified`
  `official` `team` `staff` `system` `bot` `test` `demo` `example`

> **Migration note.** A `MinLen` of 6 rejects `alice` (5) and `bob` (3), which appear
> throughout `AGENTS.md`, `apps/integration/` and the unit fixtures. The dev/test relay
> config must therefore set `NAME_MIN_LEN=3` explicitly, and `DefaultHostedPolicy()` used by
> tests keeps `MinLen = 3`. Only the **production/hosted deployment config**
> (`deploy/`) carries the 6-character default. This split is deliberate — do not "fix" the
> fixtures by renaming them.

### Availability is a first-class, pre-passkey step

`GET /hosted/availability?handle=<h>&domain=<d>` → `200` with a verdict, never a bare
boolean. The user must learn *why* before spending a WebAuthn ceremony:

```json
{
  "handle": "admin",
  "identity": "admin.poweur.net",
  "available": false,
  "reason": "reserved",
  "message": "This name is reserved by the operator.",
  "policy": { "min_len": 6, "max_len": 24, "charset": "a-z 0-9 -" }
}
```

`reason` ∈ `available | taken | reserved | blocked | too_short | too_long | charset |
hyphen | punycode | domain_not_hosted`. The endpoint is rate-limited on the existing
per-IP buckets (it is an enumeration oracle over registered identities — cheap to probe, so
cap it tighter than the message limits and never reflect anything but the verdict).
`policy` is echoed so the client can render live inline validation without hardcoding rules.

### The launcher is the same SPA on a dedicated host

**Not** a second codebase and **not** a second registrable domain. Serve the existing static
tree at a `LAUNCHER_HOST` (default `id.<hosted domain>`, e.g. `id.poweur.net`) and give
`app.js` a **no-identity mode**: when the SPA loads on the launcher host with no stored
identity, it renders the welcome + claim flow instead of the inbox.

Using `poweur.org` (a *different* registrable domain) was considered and rejected: a
credential minted there cannot be scoped to `poweur.net`, forcing a second passkey ceremony
immediately after signup. Same registrable domain is the whole point.

### Credential scope: narrow, and only for the hosted origin hop

Set `rp.id` to the **registrable domain of the identity's home**, recorded on the identity
record at creation:

- hosted `alice.poweur.net` → `rp.id = "poweur.net"` — legal (a registrable suffix of the
  origin) and usable from `id.poweur.net` *and* `alice.poweur.net`, which is exactly the hop
  the launcher introduces.
- self-hosted `bob.example.org` → `rp.id = "example.org"`, unchanged in spirit from today.

The tradeoff, stated so it is a decision and not an accident: hosted passkeys become
scoped per-**domain** rather than per-identity, so any `*.poweur.net` origin can request an
assertion for any hosted credential. Since every one of those origins is served by the same
relay under the same operator, this adds no trust boundary that did not already exist. The
WebAuthn user handle keeps identities distinct in the authenticator's account picker.

This is deliberately **not** a general "identity is bound to a domain" claim — see
[EPIC-019](EPIC-019-mobile-app-capacitor.md): because the passkey only wraps the key, a
client with its own secure storage needs no WebAuthn at all.

## Tasks

### E18-T1 — Configurable hosted name policy — **done**

- [x] `NamePolicy` in `packages/identity/namepolicy.go` with `DefaultHostedPolicy()`
      (`MinLen = 3`) and `ValidateHostedHandleWithPolicy`, returning a typed `*NameError`
      whose `Reason` T2 maps straight to a `reason` code (`ReasonOf(err)`)
- [x] ASCII LDH enforced in `validateLabel` itself, so it holds for every identity rather
      than only hosted ones; leading `xn--`, `--` runs and edge hyphens refused for hosted
      handles, where v1 has no IDN story
- [x] **Homoglyph regression test** covering Cyrillic `аdmin`, `аlice` and `pоweur`, with
      `www`/`admin` still reserved and `alice` still fine
- [x] `ReservedLabels` expanded to the grouped set; `NamePolicy.Reserved` extends it and
      cannot shorten it (an operator's own list must not be able to un-reserve `www`)
- [x] `NAME_BLOCKED_FILE` list, `substring`/`exact` matching; a missing file means no
      blocking and never blocks startup. The rejection message does **not** echo the term
      that matched — a blocklist that answers "which word?" is one you can read out
- [x] `NAME_*` wired through `config.FromEnv` into the registration path
- [x] `NAME_MIN_LEN=6` set in `docker-compose.prod.yml`; the package default of 3 stays for
      dev and the fixtures

**Two decisions the tests pinned down.** *Reserved beats length*: "admin" is 5 characters,
so a length-first order would answer `too_short` under a `MinLen` of 6 and quietly invite
the user to try "admins". And *the handle is validated before the FQDN shape*, because only
the policy produces typed reasons — shape-first turned `аdmin` into a nameless "invalid
character" rather than `charset`.

**The flags are negative (`DisallowDigits`, `DisallowHyphen`), not positive.** Every test
relay and the integration harness build a `Config` literal, so a zero-valued policy has to
mean "the usual rules"; an `AllowDigits bool` left unset would have silently rejected every
handle containing a number.

**One fixture renamed:** `verify.poweur.net` → `seedcheck.poweur.net` in
`apps/integration/seed_test.go`. `verify` is on the new reserved list, and the name was
describing the test rather than testing the name.

**Acceptance:** met — unit tests cover every reason branch and the homoglyph cases,
`apps/integration` passes with the dev policy, and `TestINT_NAME_02` boots a relay with
`MinLen: 6` that refuses `bob` and accepts `melissa`.

### E18-T2 — Availability & policy endpoint — **done**

- [x] `GET /hosted/availability?handle=&domain=` returning the verdict document, always
      `200` — every "no" is an answer, not an error. `domain` defaults to the first
      `HostedDomains` entry and must be one of them
- [x] T1's typed errors map straight to `reason`; **`taken` is checked last**, so a policy
      rejection cannot be used to ask whether a reserved handle is also registered
      (asserted directly, with `admin` both reserved *and* present in the store)
- [x] Tighter per-IP limit via a new `Limiter.AllowCost` — the endpoint charges several
      units of the same bucket messages use, which caps probing without a second limiter and
      a second set of tunables. The tradeoff is documented where it lives: a public registry
      is enumerable by design, so this is about cost, not secrecy
- [x] `apps/integration/availability_test.go` covers each reason code, and asserts the
      endpoint and `POST /identities` agree — a name the endpoint calls reserved is refused
      by registration too, or the check would be decoration
- [x] Documented in `apps/docs/docs/relay/api-reference.md`, with the policy table in
      `configuration.md`
- [x] `IdentityApi.availability()` in `@poweur/client`, so T3's live check is one call

**Acceptance:** met (`TestINT_NAME_01`): `robert` is available, is registered through the
real CLI path, and then reports `taken`; `admin` → `reserved`; `bob` → `too_short` under a
`MinLen` of 6; `аdmin` → `charset`.

### E18-T3 — Launcher host & no-identity mode — **done**

- [x] `LAUNCHER_HOST` (default `id.<first hosted domain>`), advertised at `GET /` so a
      client can tell whether it is the launcher, with `/` redirecting there to `/app/`.
      `id` and `launcher` are reserved (T1), so the host cannot be claimed
- [x] No-identity mode: the existing welcome → new-id flow *is* it, unforked. What it
      gained is the check below
- [x] Live availability as the user types (350 ms debounce, last-write-wins so a fast
      typist never sees a stale verdict), the relay's own message inline, and Next disabled
      until it says yes. A relay that cannot answer fails **open** — registration is still
      the authority and will refuse if it would have
- [x] Hand-off to the identity's own origin after a hosted claim made on the launcher host
- [x] `REGISTRATION_GATE` honoured, with the proof-of-work reported as it is solved
- [x] `test/e2e/launcher.spec.js`

**The hand-off carries the record in the URL fragment.** Storage is per-origin, so
something has to travel; a fragment is never sent to a server, and what it holds is the
same AES-GCM blob `localStorage` had — the wrapping secret comes from the passkey and is
not in it. The receiving page clears the fragment as soon as it has stored the record, so
it does not sit in the address bar or in history. The user is not asked for a second
credential because T4 scoped the passkey to the domain both hosts share.

**Found here: the registration proof-of-work gate never auto-solved.** `createIdentity`
decided whether it had been gated by looking for `pow_required` *in the error message*,
but the relay sends that as the error **code** and a human sentence as the message — so
against a real relay the string never matched and the retry never happened. It branches on
`RelayError.relayCode` now, pinned by a live-relay test that boots a relay with
`REGISTRATION_GATE=pow`. `solveRegistrationChallenge` also gained progress callbacks,
because a silent solve in front of a signup screen looks like a hang.

**Follow-ups, owned by [EPIC-015](EPIC-015-web-app-ux.md)'s second wave.** T3 gave the SPA a
launcher *host*; it did not give it a launcher *mode*. `boot()` still branches on stored
identity alone, so the same welcome card renders on `id.poweur.net`, on the apex and on
`bob.poweur.net`, and the claim form still asks for a parent domain and a hosted/DNS choice the
root document already answers. E15-T7 adds the host→mode resolution (and widens `LAUNCHER_HOST`
to a set so the apex stops serving the JSON banner), E15-T8/T9 build the two doors over it, and
E15-T10 removes the questions. No name-policy or credential-scope work is reopened here.

**Acceptance:** met in the two halves the harness can reach — `launcher.spec.js` asserts a
name is checked (reserved / taken / non-ASCII / free) before any passkey exists, and that
an identity arriving as a hand-off fragment is adopted, made active, and left locked with
the fragment cleared. The cross-origin hop itself cannot run against a test relay, which is
one host on `127.0.0.1` with no DNS for `alice.poweur.net`.

### E18-T5 — Docs — **done**

- [x] Operator guide: the [handle policy table](../apps/docs/docs/relay/configuration.md)
      (what is configuration and what is not), the blocked-terms file, and `LAUNCHER_HOST`
- [x] User-facing [claim walkthrough](../apps/docs/docs/web/claim-your-id.md), including
      why the name is checked before the passkey and what the hand-off carries
- [x] The self-hoster note: their relay serves the app at `<identity>/app/` from the
      released image — no fork, nothing to publish to an app store

### E18-T4 — Credential scope (rpId) model — **done**

- [x] `credentialRpId(identity, host)` computes the registrable domain of the identity's home
      and **falls back to the page host when the identity lives elsewhere** — a browser only
      accepts an `rp.id` that is a suffix of the current host, so asking for `poweur.net`
      from a dev relay on `127.0.0.1` is a `SecurityError`, not a wider scope. The value used
      is stored on the identity record at creation
- [x] `registrableDomain()` in `@poweur/client` (`names.ts`), a PSL-lite covering the common
      multi-label suffixes. Never returns a public suffix: `bob.co.uk` scopes to itself
- [x] **Assertions name the scope too.** A credential minted with `rp.id = poweur.net` is not
      found from `alice.poweur.net` unless the assertion asks for that scope — so
      `authenticatePasskey`, `assertChallenge` and the keystore's `rpId` argument all read the
      stored value rather than the page host
- [x] Migration: a record with no stored scope keeps being asserted against the host it was
      minted on. Unit-tested, because "upgrading" those to the registrable domain would
      silently stop finding the credential
- [x] Documented in `security/key-management.md` (with the per-domain tradeoff stated as a
      decision) and cross-linked from `protocol/web-identity.md`

**Also fixed here: the TypeScript twin had the same homoglyph bug.** `names.ts` used
`\p{L}` exactly as Go did, and no vector covered it — so the two implementations agreed
about `аdmin` only by both being wrong. The names vectors now include the homoglyph and
newly-reserved cases, which failed six conformance tests until `names.ts` was brought into
line (ASCII LDH, the expanded reserved list, and the hosted `xn--` / `--` rules).

**Acceptance:** a passkey created on `id.poweur.net` unlocks the identity on
`alice.poweur.net`; an identity record written before the change still unlocks with its
original hostname scope.

## Non-goals

- **No IDN / punycode / non-Latin handles.** v1 is ASCII LDH. Supporting other scripts needs
  a confusable-skeleton registry (UTS #39) and a bundle/variant policy — a separate epic if
  ever wanted, not a flag flip.
- **No paid or auctioned premium names**, no reservation queue, no waitlist.
- **No trademark dispute process.** The reserved list is a blunt instrument on purpose;
  disputes are an operator/legal matter outside the protocol.
- **No changes to identity documents or the registration wire format** — this is validation
  and one read-only endpoint in front of the existing `POST /identities`.
