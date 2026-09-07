# EPIC-017 — `@poweur/client`: the TypeScript client SDK

- **Status:** proposed
- **Priority:** P1 (blocks every JS-ecosystem integration; also removes the web app's copy of the protocol)
- **Depends on:** EPIC-001/002 (resolver + registration), EPIC-003/004/005 (DAV, sync, grants), EPIC-014 (PoW) — all shipped; interacts with EPIC-009 (typed messages) and EPIC-015 (web app)
- **Unlocks:** [INT-005](integrations/INT-005-agent-control-planes.md) (OpenClaw/Hermes), INT-003-T1 (MCP server), INT-003-T4 (n8n / Node-RED nodes), EPIC-010 agent SDK, and a web app that consumes the protocol instead of owning it

## Progress

| Task | Status | Notes |
|------|--------|-------|
| E17-T1 Package scaffold & workspace wiring | **open** | `packages/client-ts/`; `pnpm-workspace.yaml` currently globs `apps/*` only |
| E17-T2 Core: crypto, resolver, identity | **open** | port of `apps/web/js/crypto.js` + resolver half of `api.js`; `Signer`/`KeyStore` seams |
| E17-T3 Messaging: send, inbox, acks, anon PoW send | **open** | port of `messaging.js` + `pow.js` |
| E17-T4 Files, sync & shares | **open** | port of `files.js`; adds changes/manifest/chunked upload + grant CRUD |
| E17-T5 Go↔TS conformance vectors | **open** | the anti-drift mechanism; fixtures generated from `packages/identity` |
| E17-T6 Web app adopts the package | **open** | mirror of [E15-T6](EPIC-015-web-app-ux.md); deletes the duplicated modules |
| E17-T7 Docs, npm publish & agent quickstart | **open** | `apps/docs/docs/clients/js-sdk.md` + `@poweur/client` on npm |

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
  take `fetch` and WebCrypto from `globalThis` (present in browsers and Node ≥18), and inject
  everything else. In particular:

  ```ts
  interface Signer {          // never receives or returns raw private keys
    readonly identity: string;              // "alice.poweur.net"
    readonly publicKey: string;             // "ed25519:…"
    sign(canonicalString: string): Promise<string>;
  }
  interface KeyStore { … }    // browser: passkey/PIN-wrapped; node/agent: file or env
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
- **Conformance over convention.** The Go implementation stays canonical. `packages/identity` gains
  a test that emits JSON fixtures (canonical strings, signatures, grant documents, PoW tokens and
  solutions, identity documents); the TS suite consumes the same fixtures. A protocol change that
  lands in Go without a matching TS update fails CI — this is what makes two implementations
  sustainable.
- **Versioning.** Semver on the package, plus an exported `PROTOCOL_VERSION`. Wire-format changes
  from EPIC-009 land as minor versions with the reserved fields (`type`, `thread_id`, `metadata`)
  already typed.

## Tasks

### E17-T1 — Package scaffold & workspace wiring

- [ ] Create `packages/client-ts/` (`@poweur/client`): `tsconfig.json` (ES2022, `moduleResolution:
      bundler`-compatible output), `tsc` build to `dist/` (ESM + `.d.ts`, no bundler), Vitest config
- [ ] Add `packages/*` to `pnpm-workspace.yaml`; confirm the Go module in `packages/identity` is
      untouched by pnpm and `go work` still builds
- [ ] Root scripts (`pnpm client:build`, `client:test`) matching the existing `web:*` pattern; add
      the package to the CI workflow in `.github/`
- [ ] `exports` map with per-module subpaths; `engines.node >= 18`; no `dependencies` beyond
      `@noble/ciphers` (keep the dependency surface auditable)

**Acceptance:** `pnpm -F @poweur/client build && pnpm -F @poweur/client test` is green in CI; the
built output imports cleanly from a plain `<script type="module">` page and from `node --input-type=module`.

### E17-T2 — Core: crypto, resolver, identity

- [ ] Port `crypto.js` to typed modules (Ed25519 keygen/sign/verify, X25519 + HKDF, ChaCha20-Poly1305
      / AES-GCM seal/open), with `Signer`/`KeyStore` interfaces and a `MemoryKeyStore` for tests
- [ ] Port the resolver half of `api.js`: well-known → DNS TXT chain, `ed25519:`/`x25519:` prefix
      normalization, key pinning, SSRF guards (no redirects, size/time caps, private IPs gated by an
      explicit test flag), fail-closed on mismatch
- [ ] Identity lifecycle: create, hosted registration (signed `identity_document`, no DNS token),
      encryption-key publish, export, rotate — one function per relay endpoint that exists today
- [ ] Unit tests for happy path + failure modes per AGENTS.md (table-driven where it fits)

**Acceptance:** a Node script creates a hosted identity against a local relay and resolves it back;
tampered documents and mismatched keys are rejected with typed errors.

### E17-T3 — Messaging: send, inbox, acks, anonymous send

- [ ] `messages.send()` with both signing paths (short-lived session key by default; identity key
      opt-in, the `--sign-with=identity` equivalent for headless agents), `SessionProof` handling
- [ ] `messages.inbox()` (challenge-signed GET, decrypt) and `messages.ack()`; type the reserved
      `type` / `thread_id` / `expires_at` / `metadata` fields now so EPIC-009 is additive
- [ ] Anonymous / stranger send: port `pow.js`, expose `pow.solve()` against the challenge envelope
      (`algo` field kept open per EPIC-016) and the `/anon/{identity}` path
- [ ] Live-relay tests mirroring the existing web suite: register → session → encrypt/send → inbox
      decrypt, CLI↔TS interop in both directions

**Acceptance:** a message sent by the Go CLI decrypts in TS and vice versa, asserted against a real
relay; anonymous send solves and is accepted at the policy's difficulty.

### E17-T4 — Files, sync & shares

- [ ] DAV operations (list/read/write/mkdir/move/delete) with DAV-token minting
      (`POST /auth/dav-token`) and scoped-token support
- [ ] Sync: `changes` feed with cursor, `manifest`, chunked upload (`POST/HEAD/PATCH/DELETE
      /sync/{identity}/upload/{id}`), quota
- [ ] Shares: create/list/revoke signed grants (`poweur-sys/relay/shares/<share-id>.json`,
      audience by ID or group, read/write permissions) and read shared paths as a visitor
- [ ] `poweur-sys` document helpers (contacts, inbox policy, profile) that write the *same*
      documents the CLI writes — no client-specific state

**Acceptance:** TS creates a share for a second identity, the second identity reads the path over
DAV, revocation takes effect on the next request; grants written by TS are accepted by the relay's
signature check and listed by `poweur share ls`.

### E17-T5 — Go↔TS conformance vectors

- [ ] Go test in `packages/identity` emitting `testdata/vectors/*.json`: canonical strings +
      signatures, identity documents, share grants, group documents, PoW tokens and solutions,
      contact/policy documents
- [ ] TS conformance suite consuming the same fixtures; CI fails if a vector is unmatched or missing
- [ ] Document the rule in AGENTS.md: protocol changes regenerate vectors in the same change set

**Acceptance:** changing a canonical string in Go without touching TS turns CI red; the two
implementations cannot silently diverge.

### E17-T6 — Web app adopts `@poweur/client`

*Paired with [E15-T6](EPIC-015-web-app-ux.md) — same work, tracked from both sides.*

- [ ] `apps/web` depends on `@poweur/client` (`workspace:*`); vendor step copies `dist/` into the
      served static tree; import map gains `@poweur/client` and drops the `esm.sh` entry
- [ ] Delete `js/crypto.js`, the protocol half of `js/api.js`, `js/messaging.js`, `js/files.js`,
      `js/pow.js`; keep `passkey.js`/`storage.js` as the browser `KeyStore`/`Signer` implementation
      and `app.js` as pure UI
- [ ] Existing Vitest + Playwright suites pass unchanged (they are the regression net for this
      refactor — do not rewrite them in the same PR)

**Acceptance:** no protocol code remains in `apps/web/js` outside the key-store adapter; `pnpm
test:all` in `apps/web` is green; the relay still serves the SPA with no bundler.

### E17-T7 — Docs, npm publish & agent quickstart

- [ ] `apps/docs/docs/clients/js-sdk.md`: install, the `Signer` model, per-module API, browser vs
      Node notes; link from `clients/overview.md`
- [ ] Publish `@poweur/client` to npm (provenance-enabled release workflow), semver + changelog
- [ ] Quickstart: "a messageable agent in ~15 lines" — create identity, poll inbox, reply, attach a
      file by share — the snippet INT-005 and INT-003 pitches link to

**Acceptance:** a developer with no Go toolchain installs the package and runs the quickstart
against the demo relay.

## Non-goals

- No framework bindings (React/Vue). The package is protocol only; EPIC-015 keeps vanilla JS.
- No Python client — INT-005-T6 decides separately whether Hermes gets a port or shells out to the
  Go CLI.
- Not a replacement for the Go CLI or `packages/identity`; **Go stays canonical** and TS conforms.
- No hosted key custody. Keys stay in the caller's `KeyStore`.
