# EPIC-012 — Identity websites (`/public` → browser as a real site)

- **Status:** proposed — **design notes only; not fully specified**
- **Priority:** P2
- **Depends on:** EPIC-003 (`/pub`, layout), EPIC-006 (`poweur-sys` conventions), EPIC-007
  (inbox policy — especially anonymous / open receive), EPIC-009 (typed messages)
- **Unlocks:** personal/landing pages per ID; contact forms that become Poweur messages;
  “every ID can be a tiny website” without a separate hosting product
- **Apps note:** decide the separate site origin (E12-T1/T2) together with [EPIC-029](EPIC-029-poweur-apps-platform.md), which needs the same sandboxed sister host for app bundles. Custom domains on hosted infra and site egress are EPIC-026 entitlements; E30-T4 adds a storefront block.

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E12-T1 Hosting model decision (apex vs `www.` vs alternate) | **open** | Design notes below — pick before build |
| E12-T2 Security model (active content, CSP, cookies, sandbox) | **open** | Must not weaken `/.well-known` / DAV / app origins |
| E12-T3 Site root convention + MIME / index serving | **open** | Extends E03-T6; HTML as real HTML |
| E12-T4 Contact form → Poweur message bridge | **open** | Ties to inbox-policy anon/open modes |
| E12-T5 Spec + implementation slice | **open** | Blocked on T1–T4 decisions |

> This epic deliberately leaves options open. Do not treat the sketches below as normative
> until E12-T1/T2 land a written decision in `apps/docs/docs/files/`.

## Goal

Let an identity publish a **real browser-rendered website** from their home filesystem
(something people expect from `alice.example.org`), and make **contact / lead forms** a
first-class way to start a Poweur conversation — including, when the recipient opts in,
messages from senders who are not yet contacts (and possibly fully anonymous web posters).

Today E03-T6 only exposes marked `/public` trees at `https://<identity>/pub/…` and forces
HTML/JS to `text/plain`. This epic asks what it would take to go further, and what we must
refuse to do.

## Background (current constraints)

- Hosted IDs already resolve via a **single DNS wildcard**: `*.poweur.net → relay`
  (EPIC-002). That covers `alice.poweur.net`, not deeper labels.
- Host-routing on the relay already distinguishes identity Hosts for
  `/.well-known/poweur/`, `/dav/`, `/pub/` (EPIC-001/003).
- `/pub` is intentionally a **file share**, not a web host (stored-XSS on the identity
  origin was the hard stop).
- Messaging accepts any signed sender today; **recipient inbox policy** (contacts-only vs
  open) is EPIC-007 and not yet enforced. Anonymous (unsigned) ingress does not exist.

## Open design notes (not decisions)

### A. Where does the site live in the URL space?

Three candidate shapes — trade-offs are security + DNS + UX, not “which looks nicer”:

| Shape | Example | Pros | Cons |
|---|---|---|---|
| **A1. Apex of the ID** | `https://alice.poweur.net/` → site | One Host people already know; no extra DNS | Same origin as `/.well-known`, `/dav`, `/app`, `/pub` — XSS in user HTML can attack cookies/tokens for those paths unless carefully isolated |
| **A2. `www.<id>`** | `https://www.alice.poweur.net/` | Separate origin from apex (better cookie isolation if apex keeps APIs) | **DNS wildcard problem** — see §B |
| **A3. Path or sister host** | `https://alice.poweur.net/www/…` or `https://alice.sites.poweur.net/` | Path: no DNS change; sister host: one extra wildcard (`*.sites.poweur.net`) covers all IDs | Path still same-origin as APIs; sister host is a second hostname users must learn / link |

**Sketch preference to debate (not decided):** serve the *rendered site* on a **separate
origin** (A2 or A3 sister host) and keep the apex for protocol surfaces
(`.well-known`, DAV, messaging APIs). Marketing can still say “your site is at
alice…” if we 302 apex `/` → the site origin when a site is published — but that redirect
must not break `.well-known` or registered API paths.

### B. Can `www.<id>` be wildcarded without per-ID DNS?

**Short answer: not with a single standard DNS wildcard under the parent.**

- `*.poweur.net` matches **one** label: `alice.poweur.net` ✅, `www.alice.poweur.net` ❌.
- DNS has no `*.*.poweur.net`. Covering `www.<every-id>.poweur.net` means either:
  1. **Per-identity records** (`www.alice` CNAME/A) — defeats the “zero DNS writes”
     hosting story, or
  2. **A different hostname shape** that *is* one label deep under a dedicated parent,
     e.g. `*.sites.poweur.net` → `alice.sites.poweur.net` (ID is the leftmost label), or
  3. **Client/SNI tricks** that still need a cert story (wildcard certs also only cover
     one label: `*.poweur.net` covers apex IDs; `*.sites.poweur.net` needs its own cert).

So if we want “no DNS write per ID” **and** a separate origin for HTML, the viable
wildcard pattern is probably **`*.sites.<relay-parent>`** (or similar), not
`www.<id>.<parent>`. `www.<id>` remains attractive for self-hosted domains where the
owner already controls DNS and can add one CNAME.

Self-hosted IDs (`alice.example.org` with their own zone) are a different case: they can
point apex and/or `www` however they like; the relay only needs Host-routing + TLS.

### C. Filesystem convention

Candidate (align with EPIC-006 when chosen):

```
/public/www/          ← site root (index.html, assets/…)
  .poweur-web-public  ← or a stronger “this is an active site” marker
```

Open questions:

- Is `/public/www` special-cased, or any marked folder?
- Default document (`index.html`), clean URLs, 404 page?
- Do we still keep read-only `/pub/` file-share semantics for non-site trees
  (HTML as `text/plain`), and only enable active MIME for the site root?

### D. Security limitations we almost certainly need

Active HTML on an identity-related host is a **stored XSS / session theft** surface.
Whatever hosting shape we pick, the epic should require an explicit threat model covering:

- **Origin isolation** — prefer site origin ≠ origin that holds DAV tokens, web-app
  `localStorage`, session cookies, or passkey `rpId` if those share a parent.
- **Cookie / storage** — `Secure; HttpOnly; Path=` discipline; never set cookies for
  `/` on a host that also serves user HTML unless Path-scoped away from site trees.
  Prefer **no cookies at all** on the site origin.
- **CSP** — default locked-down Content-Security-Policy for published sites
  (e.g. no `unsafe-inline` by default; opt-in richer CSP via a site manifest). Inline
  contact-form JS may need a documented exception or a relay-served form endpoint.
- **MIME + nosniff** — already on `/pub`; keep. Only flip HTML/JS/CSS to executable
  types inside the approved site root.
- **Sandboxing options** (heavier) — serve user HTML through `sandbox` iframe on a
  relay-controlled chrome page; or separate site origin with COOP/COEP. Trade UX vs
  safety; not decided.
- **Uploads / forms** — any server-side form POST must be rate-limited, size-capped,
  CSRF-aware (SameSite / origin checks), and must not become an open relay for spam.
- **Mixed content / third-party scripts** — decide whether external CDNs are allowed;
  default deny keeps supply-chain XSS off identity pages.
- **Collision with reserved paths** — site must never shadow `/.well-known/`, `/dav/`,
  `/auth/`, `/identities`, `/messages`, `/app/`, etc. on the apex if apex hosting is chosen.

E03-T6’s “HTML as text/plain” stays the safe default until this model is approved.

### E. Contact forms → Poweur messages

Product intent: a page can include “message me” without email.

Sketch (not specified):

1. Static site posts to a **relay form endpoint** scoped to the site owner, e.g.
   `POST /forms/<owner>/contact` (or a capability URL under the site origin).
2. Relay turns the submission into a **typed message** to the owner
   (`sys.web.contact` or similar — EPIC-009 type registry), with fields
   (name, body, optional reply-to).
3. **Authenticated sender (preferred):** visitor proves a Poweur ID (sign-in lite /
   EPIC-008, or a short-lived form token minted after ID challenge). Message
   `sender` is that ID — works with normal inbox policy / contacts.
4. **Anonymous / unsigned web poster:** only accepted if the owner’s
   **inbox policy** explicitly allows it (see §F). Body is still size-capped and
   rate-limited; no encryption to a real sender key (owner-only ciphertext or
   plaintext-to-owner under relay policy — open design).
5. Easy authoring: a tiny documented snippet or relay-hosted form partial that site
   authors drop into `/public/www`, so “contact form” is copy-paste, not a custom app.

Open questions: E2E encryption for form bodies (visitor has no enc key if anon);
attachment of form files into `/shared` or inbox refs (EPIC-009); spam cost on the
*sender path* vs recipient (E07-T5).

### F. Anonymous / open receive (messaging, not only web)

This is **broader than websites** and belongs primarily in **EPIC-007** inbox policy,
with this epic as a consumer:

- Today’s sketch in E07: `contacts_only` | `contacts_and_requests` | `open`.
- Extend the policy vocabulary (proposal to refine in E07, not invent twice here), e.g.:
  - `allow_unsigned_web_forms` — accept `sys.web.contact` from the forms bridge
  - `allow_anonymous_messages` — accept unsigned or ephemeral-sender messages on the
    normal `/messages` path (dangerous; needs strong rate limits / PoW / captcha)
  - keep default **deny** for humans; bots/support addresses opt in to `open`
- Relay must **drop** anon/unsigned ingress unless the recipient’s
  `poweur-sys/relay/inbox-policy.json` says otherwise — “not dropping anon messages if
  the user wishes” is an explicit opt-in, never the default on public relays.

Web contact forms should reuse that same policy surface so operators and users learn
one knob.

## Tasks (placeholders — expand after decisions)

### E12-T1 — Choose hosting shape (apex vs `www.<id>` vs `*.sites`)

- [ ] Write a short decision record: DNS reality (§B), cert story, UX for hosted vs
      self-hosted domains
- [ ] Recommend one default for `HOSTED_DOMAINS` relays and one for self-hosted IDs
- [ ] Document how apex `/` behaves when a site is / isn’t published

**Acceptance:** decision checked into docs; epic design direction updated from “open” to
normative for the chosen shape.

### E12-T2 — Security model for active content

- [ ] Threat model: XSS → token theft, form spam, phishing chrome, path shadowing
- [ ] Required headers (CSP, COOP/COEP?, framing) and cookie rules per origin
- [ ] What user HTML may load (scripts, fonts, images) — default deny third-party

**Acceptance:** security note reviewed; E03-T6 threat section cross-linked and updated.

### E12-T3 — Site convention + serving

- [ ] Normative path (likely `/public/www`) + marker semantics vs plain `/pub` file share
- [ ] `index.html`, asset MIME (HTML/JS/CSS executable only in site root), listings off
- [ ] Integration tests: rendered HTML on site origin; apex reserved paths still win;
      unmarked trees stay non-executable

**Acceptance:** a dropped-in static site is viewable in a browser as a page (not
`text/plain` source).

### E12-T4 — Contact form bridge + inbox-policy hooks

- [ ] Spec form endpoint + `sys.web.contact` (or chosen type) payload
- [ ] Authenticated-Poweur-ID path (easy UX) and anonymous path gated by inbox policy
- [ ] Coordinate policy fields with EPIC-007 (single schema, web + general messaging)
- [ ] Rate limits, size caps, audit log for the owner

**Acceptance:** integration test — form POST becomes an inbox message when policy allows;
denied when policy is contacts-only; anon denied by default.

### E12-T5 — Docs, CLI/web helpers, migration from `/pub`

- [ ] Docs page `apps/docs/docs/files/identity-websites.md`
- [ ] Optional: `poweur site init` / web UI “Publish site” that writes marker + sample
      `index.html` + contact snippet
- [ ] Clarify relationship to E03-T6 `/pub` (file share remains; site hosting is opt-in)

## Out of scope (for now)

- Full CMS, server-side templates, or per-request compute
- Custom domains with automatic TLS beyond what the relay already does for wildcards
- Collaborative multi-author sites (use EPIC-005 sharing on `/public/www` later if needed)
- Replacing EPIC-008 sign-in chrome with “login walls” on static sites
