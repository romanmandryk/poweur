# EPIC-018 — Hosted identity onboarding: launcher, name policy & credential scope

- **Status:** in progress — T1 and T2 done; T3–T5 open
- **Priority:** P1 (the front door for every new user; blocks any public launch of a hosted domain)
- **Depends on:** EPIC-002 (hosted registration), EPIC-001 (identity documents), EPIC-014 (registration PoW gate)
- **Unlocks:** [EPIC-019](EPIC-019-mobile-app-capacitor.md) (mobile shell reuses the same onboarding), [EPIC-015](EPIC-015-web-app-ux.md) first-run flow, public availability of `*.poweur.net`

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E18-T1 Configurable name policy | **done** | `packages/identity/namepolicy.go` with typed reasons; ASCII-LDH enforcement closes the homoglyph bypass; `NAME_*` config wired into registration |
| E18-T2 Availability & policy endpoint | **done** | `GET /hosted/availability` returning a verdict + the policy; policy evaluated before registration so a rejection cannot leak whether a reserved name is taken; cost-weighted per-IP limit |
| E18-T3 Launcher host & no-identity mode | open | same SPA, dedicated host |
| E18-T4 Credential scope (rpId) model | open | narrow: hosted origin hop only |
| E18-T5 Docs | open | |

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
  carries `kdf: "prf" | "pbkdf2"`). So credential scope governs *which lock opens on which
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

### E18-T3 — Launcher host & no-identity mode

- [ ] `LAUNCHER_HOST` config (default `id.<first hosted domain>`); relay serves the existing
      SPA there; add `id` and `launcher` to the reserved labels (T1) so the host cannot be
      claimed as an identity
- [ ] No-identity mode in `app.js`: welcome → claim handle → passkey → land in the app.
      Reuse the existing `new-id` sub-page; **do not fork it**
- [ ] Live availability as the user types (debounced `GET /hosted/availability`), with the
      `reason` message inline and the passkey button disabled until `available:true` — no
      WebAuthn ceremony is ever spent on a name that will be rejected
- [ ] After successful registration, hand off to the identity's own origin
      (`https://<identity>/app/`) with the identity record; the credential from T4 opens
      there without re-enrollment
- [ ] Honour the existing `REGISTRATION_GATE` (`open`/`invite`/`pow`, EPIC-014) in this flow,
      including the PoW solver with progress
- [ ] Playwright e2e for the full launcher path

**Acceptance:** a browser with empty storage loads `id.poweur.net/app/`, claims a name,
creates a passkey, and arrives signed-in at `<name>.poweur.net/app/` — no CLI, no second
credential prompt.

### E18-T4 — Credential scope (rpId) model

- [ ] Compute `rp.id` as the registrable domain of the identity's home; store it on the
      identity record so unlock uses the same value it was created with
- [ ] Registrable-domain helper shared by `apps/web` and `@poweur/client` — a small PSL-lite
      that handles the common multi-label suffixes (`co.uk`, `com.au`); an operator on an
      exotic suffix can override via config rather than shipping the full PSL
- [ ] Migration for identities created before this change (stored `rpId` absent ⇒ fall back to
      the identity hostname, which is what they were minted with) — **existing passkeys must
      keep working**; cover with a unit test
- [ ] Document the per-domain scope tradeoff in `apps/docs/docs/protocol/` and cross-link from
      EPIC-011

**Acceptance:** a passkey created on `id.poweur.net` unlocks the identity on
`alice.poweur.net`; an identity record written before the change still unlocks with its
original hostname scope.

### E18-T5 — Docs

- [ ] Operator guide: choosing a name policy, the blocked-terms file, running a launcher host
- [ ] User-facing "claim your ID" walkthrough in `apps/docs/docs/web/`
- [ ] Note in the self-hosting guide that a self-hoster's relay serves the app at
      `<identity>/app/` from the released image — **no fork, no app-store publishing**

## Non-goals

- **No IDN / punycode / non-Latin handles.** v1 is ASCII LDH. Supporting other scripts needs
  a confusable-skeleton registry (UTS #39) and a bundle/variant policy — a separate epic if
  ever wanted, not a flag flip.
- **No paid or auctioned premium names**, no reservation queue, no waitlist.
- **No trademark dispute process.** The reserved list is a blunt instrument on purpose;
  disputes are an operator/legal matter outside the protocol.
- **No changes to identity documents or the registration wire format** — this is validation
  and one read-only endpoint in front of the existing `POST /identities`.
