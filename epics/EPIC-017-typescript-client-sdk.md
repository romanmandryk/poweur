# EPIC-017 — `@poweur/client`: the TypeScript client SDK

- **Status:** in progress — core, messaging, files/sync/shares, conformance and the CLI shipped; web adoption (E17-T6) and npm publish (E17-T7 publish step) open
- **Priority:** P1 (blocks every JS-ecosystem integration; also removes the web app's copy of the protocol)
- **Depends on:** EPIC-001/002 (resolver + registration), EPIC-003/004/005 (DAV, sync, grants), EPIC-014 (PoW) — all shipped; interacts with EPIC-009 (typed messages) and EPIC-015 (web app)
- **Unlocks:** [INT-005](integrations/INT-005-agent-control-planes.md) (OpenClaw/Hermes), INT-003-T1 (MCP server), INT-003-T4 (n8n / Node-RED nodes), EPIC-010 agent SDK, and a web app that consumes the protocol instead of owning it

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E17-T1 Package scaffold & workspace wiring | **done** | `packages/client-ts/`; `packages/*` added to the pnpm globs; root `client:*` + `vectors` scripts; `.github/workflows/ci.yml` |
| E17-T2 Core: crypto, resolver, identity | **done** | `Signer`/`KeyStore`/`Decryptor` seams; fail-closed resolver with SSRF guards; 33 unit tests |
| E17-T3 Messaging: send, inbox, acks, anon PoW send | **done** | both signing paths, `SessionProof`, tick-2 acks, anonymous send with PoW; 16 live-relay tests |
| E17-T4 Files, sync & shares | **done** | DAV + tokens, changes/manifest/chunked upload, grants and groups, `poweur-sys` helpers; 26 live-relay tests |
| E17-T5 Go↔TS conformance vectors | **done** | 3 Go generators → `packages/identity/testdata/vectors/`; 60 TS conformance tests; rule recorded in AGENTS.md |
| E17-T6 Web app adopts the package | **done** | mirror of [E15-T6](EPIC-015-web-app-ux.md); `apps/web` vendors the built ESM, owns no protocol code, and surfaced the unbound-`fetch` browser bug fixed in `http.ts` |
| E17-T7 Docs, npm publish & agent quickstart | **partly done** | `apps/docs/docs/clients/js-sdk.md` + quickstart + overview link shipped; **npm publish and the release workflow are open** |
| E17-T8 CLI parity with the Go CLI | **done** | *added* — `poweur` bin, every Go command including `key enroll|approve|claim`, shared `~/.poweur` tree; Go↔TS interop covers messaging and the new-device enrollment ceremony |

## Goal

Extract the working browser client in `apps/web/js/` into a **versioned, published, runtime-agnostic
TypeScript package** — `@poweur/client` — that runs in the browser, in Node ≥18 and in Bun/Deno,
covering identity, messaging, files, sync, shares and proof-of-work.

Two payoffs, and the second is the reason this is P1:

1. **The web app stops owning the protocol.** Today `apps/web/js` *is* the reference JS
   implementation; every fix has to be made there and then hand-copied by anyone else. After this
   epic the web app is a consumer like any other, importing the package as a workspace dependency.
2. **Every JS-ecosystem integration becomes a thin adapter.** The OpenClaw channel plugin
   (INT-005), the MCP server (INT-003-T1), n8n and Node-RED nodes (INT-003-T4) are all TypeScript
   and all need exactly this: send a message, read an inbox, write a file, mint a share. Without a
   package each of them either re-implements Ed25519 canonical signing or shells out to the Go CLI.
   With it, each is a few hundred lines of glue.

## Background (current code)

- `apps/web/js/` already contains a complete, tested client: `crypto.js` (Ed25519 sign/verify,
  X25519 + HKDF + ChaCha20-Poly1305 / AES-GCM), `api.js` (resolver chain — well-known first, then
  DNS TXT, with `ed25519:` / `x25519:` prefix normalization — plus registration and challenge-signed
  GETs), `messaging.js` (session vs `--sign-with=identity` send, inbox decrypt), `files.js`
  (DAV list/upload/download/mkdir/rename/delete), `pow.js` (JS twin of `packages/identity/pow.go`),
  `storage.js`, `passkey.js`.
- Its tests are the model to keep: Vitest unit tests **plus** suites that spawn a real relay
  (`go run` with `POWEUR_DATA`/`HOSTED_DOMAINS`/`WEB_STATIC_DIR`) — protocol behavior is asserted
  against the real server, not a mock.
- The web app resolves bare specifiers with an **import map** (`apps/web/index.html`), today
  pointing `@noble/ciphers/chacha.js` at `esm.sh`. There is no bundler and
  [EPIC-015](EPIC-015-web-app-ux.md) explicitly wants to keep it that way.
- `pnpm-workspace.yaml` globs `apps/*` only; `packages/` currently holds the Go
  `packages/identity` module. Both need to coexist.
- Drift is already a real cost: `pow.js` and `pow.go` are hand-maintained twins, and canonical
  signing strings exist in Go (`packages/identity`), in Go again (CLI) and in JS. A third
  hand-written copy per integration is the failure mode this epic exists to prevent.

## Design direction

- **Location & name.** Source at `packages/client-ts/` (keeps language-shared code under
  `packages/`, alongside `packages/identity`), published as `@poweur/client`. Add `packages/*` to
  the pnpm workspace globs; the Go module in `packages/identity` is unaffected (no `package.json`,
  so pnpm ignores it).
- **Runtime-agnostic core, injected environment.** No DOM APIs and no Node builtins in the core:
  take `fetch` from `globalThis` and inject everything else. **Changed during
  implementation:** the original plan took Ed25519/X25519 from WebCrypto and allowed no dependency
  beyond `@noble/ciphers`. That does not hold — WebCrypto's Ed25519 and X25519 are absent on Node 18
  and early 20, and on Safari &lt; 17, so "runs on every runtime" and "WebCrypto only" are mutually
  exclusive. The package therefore depends on `@noble/curves`, `@noble/hashes` and `@noble/ciphers`
  (one author, audited, ~40KB total) and behaves identically everywhere. `@noble/hashes` also
  supplies argon2id for app passwords, which WebCrypto has no equivalent for at all. In particular:

  ```ts
  interface Signer {          // never receives or returns raw private keys
    readonly identity: string;              // "alice.poweur.net"
    readonly publicKey: string;             // "ed25519:…"
    sign(canonicalString: string): Promise<string>;
  }
  interface KeyStore { … }    // browser: passkey-PRF-wrapped; node/agent: file or env
  ```

  This is what lets a passkey-gated browser key and an unattended agent key (`--sign-with=identity`
  semantics) drive the same code paths — and it is the seam INT-005 needs so a gateway can hold an
  *agent's* key without ever holding the operator's.
- **Layered, tree-shakeable modules**, mirroring the docs structure: `crypto`, `resolve`,
  `identity`, `messages`, `files`, `sync`, `shares`, `contacts`, `policy`, `pow`. An integration
  that only sends messages must not pull in DAV.
- **Fail-closed by default, same as Go.** Key mismatch during resolution is an error, not a warning;
  no redirects and no private IPs on well-known fetches unless the test flag is set. The TS resolver
  reuses the Go rules verbatim — see E17-T5.
- **Zero-build consumption for the web app.** Ship ESM plus `.d.ts` (build with `tsc`, no bundler),
  and have `apps/web` vendor the built ESM into its static tree with an import-map entry
  (`"@poweur/client": "/app/vendor/poweur-client/index.js"`). EPIC-015's "no framework, no build
  step" constraint holds: a copy step in the dev/test harness, not a bundler in the request path.
  Vendoring also removes the runtime `esm.sh` fetch for `@noble/ciphers`, which is a supply-chain
  and offline-startup liability in the current import map.
- **Conformance over convention.** The Go implementation stays canonical. Go tests emit JSON
  fixtures (canonical strings, signatures, grant documents, PoW solutions, identity documents,
  sealed payloads); the TS suite consumes the same fixtures. A protocol change that lands in Go
  without a matching TS update fails CI — this is what makes two implementations sustainable.
  **Refined during implementation:** the generators had to be split three ways rather than living
  only in `packages/identity`, because the canonical *signing strings* live in
  `apps/api/internal/crypto` and message *encryption* lives in `apps/cli/internal/crypto`, and Go's
  `internal/` rule blocks cross-module imports. Each generator now sits beside the code it pins and
  writes into the one shared `packages/identity/testdata/vectors/` directory.
- **One `~/.poweur` tree, two CLIs.** The TS CLI reads and writes the Go CLI's own files rather than
  a parallel store, so an identity created by either is immediately usable by the other. This needed
  a small dependency-free TOML subset reader/writer that round-trips with `go-toml` — including
  unquoted RFC 3339 datetimes, which a quoted value would break on Go's `time.Time` unmarshal.
- **Versioning.** Semver on the package, plus an exported `PROTOCOL_VERSION`. Wire-format changes
  from EPIC-009 land as minor versions with the reserved fields (`type`, `thread_id`, `metadata`)
  already typed.

## Tasks

### E17-T1 — Package scaffold & workspace wiring

- [x] Create `packages/client-ts/` (`@poweur/client`): `tsconfig.json` (ES2022, `moduleResolution:
      bundler`-compatible output), `tsc` build to `dist/` (ESM + `.d.ts`, no bundler), Vitest config
- [x] Add `packages/*` to `pnpm-workspace.yaml`; confirm the Go module in `packages/identity` is
      untouched by pnpm and `go work` still builds
- [x] Root scripts (`pnpm client:build`, `client:test`) matching the existing `web:*` pattern; add
      the package to the CI workflow in `.github/`
- [x] `exports` map with per-module subpaths; `engines.node >= 18`; no `dependencies` beyond
      `@noble/ciphers` (keep the dependency surface auditable)

**Acceptance:** `pnpm -F @poweur/client build && pnpm -F @poweur/client test` is green in CI; the
built output imports cleanly from a plain `<script type="module">` page and from `node --input-type=module`. ✅

### E17-T2 — Core: crypto, resolver, identity

- [x] Port `crypto.js` to typed modules (Ed25519 keygen/sign/verify, X25519 + HKDF, ChaCha20-Poly1305
      / AES-GCM seal/open), with `Signer`/`KeyStore` interfaces and a `MemoryKeyStore` for tests
- [x] Port the resolver half of `api.js`: well-known → DNS TXT chain, `ed25519:`/`x25519:` prefix
      normalization, key pinning, SSRF guards (no redirects, size/time caps, private IPs gated by an
      explicit test flag), fail-closed on mismatch
- [x] Identity lifecycle: create, hosted registration (signed `identity_document`, no DNS token),
      encryption-key publish, export, rotate — one function per relay endpoint that exists today
- [x] Unit tests for happy path + failure modes per AGENTS.md (table-driven where it fits)

**Acceptance:** a Node script creates a hosted identity against a local relay and resolves it back;
tampered documents and mismatched keys are rejected with typed errors. ✅ (`test/messaging-relay.test.ts`, `test/resolve.test.ts`)

### E17-T3 — Messaging: send, inbox, acks, anonymous send

- [x] `messages.send()` with both signing paths (short-lived session key by default; identity key
      opt-in, the `--sign-with=identity` equivalent for headless agents), `SessionProof` handling
- [x] `messages.inbox()` (challenge-signed GET, decrypt) and `messages.ack()`; type the reserved
      `type` / `thread_id` / `expires_at` / `metadata` fields now so EPIC-009 is additive
- [x] Anonymous / stranger send: port `pow.js`, expose `pow.solve()` against the challenge envelope
      (`algo` field kept open per EPIC-016) and the `/anon/{identity}` path
- [x] Live-relay tests mirroring the existing web suite: register → session → encrypt/send → inbox
      decrypt, CLI↔TS interop in both directions

**Acceptance:** a message sent by the Go CLI decrypts in TS and vice versa, asserted against a real
relay; anonymous send solves and is accepted at the policy's difficulty. ✅ (`test/cli-interop.test.ts`, `test/messaging-relay.test.ts`)

### E17-T4 — Files, sync & shares

- [x] DAV operations (list/read/write/mkdir/move/delete) with DAV-token minting
      (`POST /auth/dav-token`) and scoped-token support
- [x] Sync: `changes` feed with cursor, `manifest`, chunked upload (`POST/HEAD/PATCH/DELETE
      /sync/{identity}/upload/{id}`), quota
- [x] Shares: create/list/revoke signed grants (`poweur-sys/relay/shares/<share-id>.json`,
      audience by ID or group, read/write permissions) and read shared paths as a visitor
- [x] `poweur-sys` document helpers (contacts, inbox policy, profile) that write the *same*
      documents the CLI writes — no client-specific state
- [x] Contact *lifecycle* on `PoweurClient`: `requestContact` / `acceptContact` /
      `blockContact` (added for EPIC-015 E15-T2). The document write and the typed
      `sys.contact.*` envelope have to happen together and in order, and that composition
      previously lived only in the TS CLI's command layer, where the web app could not
      reach it. Both CLIs now call these, which also re-aligns a Go↔TS divergence: the Go
      `contacts request` pinned the resolved key, the TS one did not
      (`test/contacts-relay.test.ts`)

**Acceptance:** TS creates a share for a second identity, the second identity reads the path over
DAV, revocation takes effect on the next request; grants written by TS are accepted by the relay's
signature check and listed by `poweur share ls`. ✅ (`test/files-relay.test.ts`)

### E17-T5 — Go↔TS conformance vectors

- [x] Go tests emitting `testdata/vectors/*.json`: canonical strings +
      signatures, identity documents, share grants, group documents, PoW tokens and solutions,
      contact/policy documents
- [x] TS conformance suite consuming the same fixtures; CI fails if a vector is unmatched or missing
- [x] Document the rule in AGENTS.md: protocol changes regenerate vectors in the same change set

**Acceptance:** changing a canonical string in Go without touching TS turns CI red; the two
implementations cannot silently diverge. ✅ — 60 conformance tests; the vector generators caught two
real mismatches on their first run (an encryption-envelope field-name difference and a `null` vs
empty members array in group canonicalization).

### E17-T6 — Web app adopts `@poweur/client`

*Paired with [E15-T6](EPIC-015-web-app-ux.md) — same work, tracked from both sides.*

- [x] `apps/web` depends on `@poweur/client` (`workspace:*`); `apps/web/scripts/vendor.mjs` copies
      the reachable ESM graph into the served static tree, rewriting bare specifiers to relative
      paths; import map gains `@poweur/client` and drops the `esm.sh` entry
- [x] Delete `js/crypto.js`, the protocol half of `js/api.js`, `js/messaging.js`, `js/files.js`,
      `js/pow.js`; `passkey.js`/`storage.js`/`vault.js` are the browser `KeyStore`/`Signer`
      implementation and `app.js` is pure UI
- [x] `test/e2e/hosted.spec.js` passes unchanged; the Vitest protocol suites were retired in favour
      of adapter and live-relay suites over the app's own modules — see E15-T6 for the reasoning

**Browser bug this adoption caught:** `RelayClient`, `resolveIdentity` and `dohTxtResolver` all did
`options.fetch ?? globalThis.fetch`, capturing `fetch` unbound. Node calls that fine; a browser
throws `Illegal invocation`, so the very first page load failed at registration. `defaultFetch()`
in `http.ts` binds it, and `test/fetch-binding.test.ts` reproduces the browser's receiver rule so
Node tests can catch a regression.

**Acceptance:** no protocol code remains in `apps/web/js` outside the key-store adapter; `pnpm
test:all` in `apps/web` is green; the relay still serves the SPA with no bundler.

### E17-T7 — Docs, npm publish & agent quickstart

- [x] `apps/docs/docs/clients/js-sdk.md`: install, the `Signer` model, per-module API, browser vs
      Node notes; link from `clients/overview.md`
- [ ] Publish `@poweur/client` to npm (provenance-enabled release workflow), semver + changelog
- [x] Quickstart: "a messageable agent in ~15 lines" — create identity, poll inbox, reply, attach a
      file by share — the snippet INT-005 and INT-003 pitches link to

**Acceptance:** a developer with no Go toolchain installs the package and runs the quickstart
against the demo relay. ⏳ blocked on the npm publish step only; the quickstart itself is written and
runs from a local checkout.

### E17-T8 — CLI parity with the Go CLI *(added during implementation)*

Not in the original plan: the epic scoped a library, but a library alone leaves every JS user either
installing Go or hand-rolling a CLI. `npx @poweur/client` closes that, and sharing the Go CLI's own
state directory means the two are interchangeable rather than merely similar.

- [x] `poweur` bin (`npx @poweur/client …`), dependency-free arg parsing, `--json` on every command
- [x] Every Go command: `identity` (create/show/dns/use/list/add-encryption-key/lookup/export),
      `key rotate`, `send` (session/identity/anon, `--via-home-relay`, `--type`, `--accept-new-key`),
      `inbox`, `messages status`, `anon`, `session`, `relay`, `dav` (token/mount/password),
      `sync`, `share`, `contacts`, `requests`, `policy`, `auth`
- [x] Shared `~/.poweur` tree: `config.toml`, `keys/<id>.key|.enc`, `sessions/<id>.toml`,
      `pending/<id>.jsonl` — same formats, same permissions (0600/0700)
- [x] `POWEUR_RESOLVER_DIAL` parity: a `node:http`-based resolver fetch that dials a fixed address
      while keeping the identity as the Host header (WHATWG `fetch` forbids setting `Host`)
- [x] Go↔TS interop tests: a message sent by either client decrypts in the other, both read one
      config, and a session written by TS is honoured by the Go CLI

**Acceptance:** `npx @poweur/client` runs every documented command; the interop suite proves both
directions against a real relay. ✅

## Parity limits (measured, not assumed)

Everything in `poweur --help` is reachable from TypeScript. Four things are not at *full* parity, by
nature rather than omission:

| Area | Limit |
|------|-------|
| `poweur sync` local reconciliation | Node/Bun/Deno only — it walks a real filesystem. Browsers get the remote half (`changes`, `manifest`, chunked upload) via `SyncClient`. |
| DNS TXT in the browser | Browsers cannot do DNS. The browser build uses DNS-over-HTTPS, a different trust anchor: the DoH provider sees the lookups and answers instead of the system resolver. Node uses `node:dns` and is at true parity. |
| PoW solving speed | Same algorithm, ~5–10× slower than Go (≈1s vs ≈5–10s at 20 bits). Functional parity, not performance parity. Documented as a UI warning threshold. |
| `auth sign <file>`, `identity export --out` | Node-only, because they read and write local files. The in-memory equivalents work everywhere. |

## Deferred

- **E17-T6 (web app adoption)** is deliberately left for its own change set. It deletes five modules
  from `apps/web/js` and re-points the import map, and its regression net is the existing Vitest +
  Playwright suites — which must pass *unchanged*. Mixing that refactor into the change set that
  introduced the package would make a failure ambiguous between "the SDK is wrong" and "the
  migration is wrong". Tracked from both sides: [E15-T6](EPIC-015-web-app-ux.md).
- **npm publish (part of E17-T7).** The package, docs and quickstart are done; publishing needs an
  npm org, a provenance-enabled release workflow and a first tagged version — an operational step,
  not a code one.

## Non-goals

- No framework bindings (React/Vue). The package is protocol only; EPIC-015 keeps vanilla JS.
- No Python client — INT-005-T6 decides separately whether Hermes gets a port or shells out to the
  Go CLI.
- Not a replacement for the Go CLI or `packages/identity`; **Go stays canonical** and TS conforms.
- No hosted key custody. Keys stay in the caller's `KeyStore`.
