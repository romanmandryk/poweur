/**
 * Sign in with Poweur ID — TypeScript verifier SDK (EPIC-008 E08-T2).
 *
 * Two halves:
 *
 *  1. **Conformance.** Every case in `packages/identity/testdata/vectors/signin.json`
 *     is replayed through the TypeScript verifier. Go decided what a verifier
 *     standing at that origin at that instant must answer; if TypeScript answers
 *     differently, one of the two implementations is wrong and this fails.
 *  2. **Unit.** The rules the vectors do not reach — origin normalization, scope
 *     namespacing, the nonce cache's atomicity contract, RP metadata fetching,
 *     the signer, and every failure mode of each.
 */

import { describe, expect, it } from "vitest";

import { generateSigningKeypair, signCanonical } from "../src/crypto/index.js";
import { toBase64url } from "../src/encoding.js";
import {
  MemoryNonceCache,
  SignInVerifier,
  canonicalSignInResponse,
  checkRequestAgainstMetadata,
  checkSignInScopeNamespace,
  decodeSignInRequest,
  describeScope,
  encodeSignInRequest,
  fetchRelyingPartyMetadata,
  metadataAllowsResponseUri,
  normalizeOrigin,
  normalizeSignInScope,
  normalizeSignInScopes,
  sameOrigin,
  signInAppId,
  signInDeepLink,
  signInResponseCanonical,
  signInWebLink,
  signSignInRequest,
  summarizeSignInRequest,
  validateRelyingPartyMetadata,
  validateSignInRequest,
  verifySessionProof,
  type RelyingPartyMetadata,
  type SignInRequest,
  type SignInResponse,
} from "../src/signin.js";
import { canonicalSessionRegistration } from "../src/canonical.js";
import type { IdentityDocument, ResolveResult } from "../src/types.js";
import { loadVectors } from "./vectors.js";

// ── Conformance ──────────────────────────────────────────────────────────────

interface SignInVectorFile {
  identity_key: string;
  session_key: string;
  session_registration_canonical: string;
  now: string;
  vectors: {
    name: string;
    origin: string;
    response: SignInResponse;
    canonical: string;
    valid: boolean;
    reason?: string;
    replay_of?: string;
  }[];
}

function documentFor(identity: string, publicKey: string): IdentityDocument {
  return {
    version: 1,
    identity,
    public_key: publicKey,
    relay: "https://relay.poweur.net",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

describe("sign-in conformance with Go vectors", () => {
  const file = loadVectors<SignInVectorFile>("signin");
  const nowMs = Date.parse(file.now);

  it("derives the same session-registration string Go signed", () => {
    // The delegation proof rides on the relay's existing string; a drift here
    // would break session-signed messages, not just sign-in.
    const proof = file.vectors.find((v) => v.name === "valid-session-delegated")!.response
      .session_proof!;
    expect(
      canonicalSessionRegistration(
        "alice.poweur.net",
        proof.session_public_key,
        proof.issued_at,
        proof.expires_at,
        proof.nonce,
      ),
    ).toBe(file.session_registration_canonical);
  });

  for (const vector of file.vectors) {
    it(`re-derives the canonical string for "${vector.name}"`, () => {
      expect(signInResponseCanonical(vector.response)).toBe(vector.canonical);
    });
  }

  for (const vector of file.vectors) {
    it(`${vector.valid ? "accepts" : `rejects (${vector.reason})`} "${vector.name}"`, async () => {
      const verifier = new SignInVerifier({
        origin: vector.origin,
        now: () => nowMs,
        resolve: async (name): Promise<ResolveResult> => ({
          document: documentFor(name, file.identity_key),
          source: "web",
        }),
      });

      // A replay vector is only a replay once its twin has been spent, so run
      // the original through the same verifier first.
      if (vector.replay_of) {
        const first = file.vectors.find((v) => v.name === vector.replay_of)!;
        await expect(verifier.verifyResponse(first.response)).resolves.toBeTruthy();
      }

      if (!vector.valid) {
        await expect(verifier.verifyResponse(vector.response)).rejects.toThrow();
        return;
      }
      const result = await verifier.verifyResponse(vector.response);
      expect(result.identity).toBe("alice.poweur.net");
      expect(result.audience).toBe(vector.origin);
      expect(result.appId).toBe("net.poweur.guestbook");
      expect(result.sessionDelegated).toBe(vector.name === "valid-session-delegated");
      expect(result.relay).toBe("https://relay.poweur.net");
    });
  }

  it("round-trips every vector response through the encoded form", async () => {
    for (const vector of file.vectors.filter((v) => v.valid)) {
      const verifier = new SignInVerifier({
        origin: vector.origin,
        now: () => nowMs,
        resolve: async (name) => ({ document: documentFor(name, file.identity_key), source: "web" }),
      });
      const encoded = toBase64url(new TextEncoder().encode(JSON.stringify(vector.response)));
      await expect(verifier.verify(encoded)).resolves.toBeTruthy();
    }
  });
});

// ── Origins ──────────────────────────────────────────────────────────────────

describe("origin normalization", () => {
  const ok: [string, string][] = [
    ["https://Guestbook.Poweur.NET", "https://guestbook.poweur.net"],
    ["https://guestbook.poweur.net:443", "https://guestbook.poweur.net"],
    ["https://guestbook.poweur.net/", "https://guestbook.poweur.net"],
    ["http://localhost:8080", "http://localhost:8080"],
    ["http://localhost:80", "http://localhost"],
    ["https://example.com:8443", "https://example.com:8443"],
  ];
  for (const [raw, want] of ok) {
    it(`normalizes ${raw}`, () => expect(normalizeOrigin(raw)).toBe(want));
  }

  // An origin is not a URL. Everything below could be used to make two
  // different strings compare equal to a sloppy verifier.
  const bad = [
    "",
    "guestbook.poweur.net",
    "ftp://guestbook.poweur.net",
    "https://guestbook.poweur.net/callback",
    "https://guestbook.poweur.net?a=1",
    "https://guestbook.poweur.net#x",
    "https://user:pw@guestbook.poweur.net",
    "https://",
  ];
  for (const raw of bad) {
    it(`rejects ${JSON.stringify(raw)}`, () => expect(() => normalizeOrigin(raw)).toThrow());
  }

  it("compares response_uri against the origin, not the string", () => {
    expect(sameOrigin("https://rp.example", "https://rp.example/auth/callback")).toBe(true);
    expect(sameOrigin("https://rp.example", "https://rp.example:443/x")).toBe(true);
    expect(sameOrigin("https://rp.example", "https://rp.example.evil.test/x")).toBe(false);
    expect(sameOrigin("https://rp.example", "http://rp.example/x")).toBe(false);
    expect(sameOrigin("https://rp.example", "not a url")).toBe(false);
  });

  it("derives the app namespace by reversing host labels", () => {
    expect(signInAppId("https://guestbook.poweur.net")).toBe("net.poweur.guestbook");
    expect(signInAppId("https://example.com")).toBe("com.example");
    expect(() => signInAppId("https://localhost")).toThrow();
  });
});

// ── Scopes ───────────────────────────────────────────────────────────────────

describe("scopes", () => {
  it("normalizes dav paths to one form", () => {
    expect(normalizeSignInScope("dav:rw:/apps/net.example/")).toBe("dav:rw:apps/net.example");
    expect(normalizeSignInScope(" dav:read:apps/net.example ")).toBe("dav:read:apps/net.example");
    expect(normalizeSignInScope("profile:read")).toBe("profile:read");
  });

  for (const bad of ["", "dav:rw:", "dav:rw:/", "dav:rw:apps/../poweur-sys", "dav:rw:a//b", "storage:all"]) {
    it(`rejects ${JSON.stringify(bad)}`, () => expect(() => normalizeSignInScope(bad)).toThrow());
  }

  it("sorts and de-duplicates so the canonical string is order-independent", () => {
    expect(normalizeSignInScopes(["profile:read", "dav:rw:/apps/x.y/", "dav:rw:apps/x.y"])).toEqual([
      "dav:rw:apps/x.y",
      "profile:read",
    ]);
  });

  it("caps the consent screen at 16 scopes", () => {
    const many = Array.from({ length: 17 }, (_, i) => `dav:rw:apps/net.example/${i}`);
    expect(() => normalizeSignInScopes(many)).toThrow();
  });

  it("keeps a dav scope inside the app's own namespace", () => {
    expect(() => checkSignInScopeNamespace("dav:rw:apps/net.example", "net.example")).not.toThrow();
    expect(() => checkSignInScopeNamespace("dav:rw:apps/net.example/notes", "net.example")).not.toThrow();
    // The escalation attempts: a sibling app, the parent, and a prefix twin.
    expect(() => checkSignInScopeNamespace("dav:rw:apps/net.other", "net.example")).toThrow();
    expect(() => checkSignInScopeNamespace("dav:rw:apps", "net.example")).toThrow();
    expect(() => checkSignInScopeNamespace("dav:rw:apps/net.example.evil", "net.example")).toThrow();
    expect(() => checkSignInScopeNamespace("dav:rw:poweur-sys", "net.example")).toThrow();
    // Non-dav scopes carry no path and are unaffected.
    expect(() => checkSignInScopeNamespace("profile:read", "net.example")).not.toThrow();
  });
});

// ── Requests, signing, verifying ─────────────────────────────────────────────

const RP_ORIGIN = "https://guestbook.poweur.net";
const WHO = "alice.poweur.net";
const NOW = Date.parse("2026-01-15T09:30:00Z");

function newIdentity() {
  const kp = generateSigningKeypair();
  return {
    privateKey: kp.privateKey,
    document: documentFor(WHO, toBase64url(kp.publicKey)),
  };
}

function verifierFor(doc: IdentityDocument, origin = RP_ORIGIN, now = () => NOW) {
  return new SignInVerifier({
    origin,
    now,
    resolve: async () => ({ document: doc, source: "web" }),
  });
}

describe("request construction", () => {
  it("builds a request bound to its own origin", () => {
    const v = verifierFor(newIdentity().document);
    const req = v.newRequest({ statement: "Sign in to the Guestbook" });
    expect(req.audience).toBe(RP_ORIGIN);
    expect(req.domain).toBe("guestbook.poweur.net");
    expect(req.action).toBe("signin");
    expect(req.request_id.startsWith("req_")).toBe(true);
    expect(Date.parse(req.expires_at) - Date.parse(req.issued_at)).toBe(2 * 60_000);
  });

  it("never issues a window longer than five minutes", () => {
    const v = verifierFor(newIdentity().document);
    const req = v.newRequest({ ttlMs: 60 * 60_000 });
    expect(Date.parse(req.expires_at) - Date.parse(req.issued_at)).toBe(5 * 60_000);
  });

  it("refuses a response_uri that is not its own origin", () => {
    const v = verifierFor(newIdentity().document);
    expect(() => v.newRequest({ responseUri: "https://evil.example/collect" })).toThrow();
  });

  it("refuses to ask for another app's namespace", () => {
    const v = verifierFor(newIdentity().document);
    expect(() => v.newRequest({ scopes: ["dav:rw:apps/net.poweur.mail"] })).toThrow();
  });

  it("round-trips through the deep link and web link forms", () => {
    const v = verifierFor(newIdentity().document);
    const req = v.newRequest({ statement: "hello" });
    expect(decodeSignInRequest(encodeSignInRequest(req))).toEqual(req);
    const deep = signInDeepLink(req);
    expect(deep.startsWith("poweur://auth?request=")).toBe(true);
    expect(decodeSignInRequest(deep.slice("poweur://auth?request=".length))).toEqual(req);
    const web = signInWebLink("https://poweur.net/app/", req);
    const auth = new URL(web).searchParams.get("auth")!;
    expect(decodeSignInRequest(auth)).toEqual(req);
    expect(signInWebLink("https://poweur.net/app/?x=1", req)).toContain("&auth=");
  });

  it("accepts raw JSON as well as base64url", () => {
    const v = verifierFor(newIdentity().document);
    const req = v.newRequest({});
    expect(decodeSignInRequest(JSON.stringify(req))).toEqual(req);
    expect(() => decodeSignInRequest("")).toThrow();
    expect(() => decodeSignInRequest("!!!not base64!!!")).toThrow();
  });

  const badRequests: [string, Partial<SignInRequest>][] = [
    ["unknown version", { poweur_auth: "2" }],
    ["missing nonce", { nonce: "" }],
    ["missing request_id", { request_id: "" }],
    ["unknown action", { action: "transfer" }],
    ["multi-line statement", { statement: "line one\nline two" }],
    ["over-long statement", { statement: "x".repeat(301) }],
    ["expires before issue", { issued_at: "2026-01-15T09:31:00Z", expires_at: "2026-01-15T09:29:00Z" }],
    ["window over 5 minutes", { expires_at: "2026-01-15T09:40:00Z" }],
    ["already expired", { issued_at: "2026-01-15T09:00:00Z", expires_at: "2026-01-15T09:02:00Z" }],
    ["issued far in the future", { issued_at: "2026-01-15T09:40:00Z", expires_at: "2026-01-15T09:44:00Z" }],
    ["off-origin response_uri", { response_uri: "https://evil.example/cb" }],
    ["unparseable timestamps", { issued_at: "yesterday", expires_at: "tomorrow" }],
  ];
  for (const [name, patch] of badRequests) {
    it(`rejects a request with ${name}`, () => {
      const req: SignInRequest = {
        poweur_auth: "1",
        request_id: "req_x",
        audience: RP_ORIGIN,
        nonce: "n",
        issued_at: "2026-01-15T09:29:00Z",
        expires_at: "2026-01-15T09:31:00Z",
        action: "signin",
        ...patch,
      };
      expect(() => validateSignInRequest(req, NOW)).toThrow();
    });
  }
});

describe("signing and verifying", () => {
  it("signs and verifies a login end to end", async () => {
    const id = newIdentity();
    const v = verifierFor(id.document);
    const req = v.newRequest({ statement: "Sign in to the Guestbook", scopes: ["profile:read"] });
    const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
    const result = await v.verify(encodeSignInRequest(resp) /* same encoding for both */);
    expect(result.identity).toBe(WHO);
    expect(result.scopes).toEqual(["profile:read"]);
    expect(result.sessionDelegated).toBe(false);
    expect(result.requestId).toBe(req.request_id);
  });

  it("copies the challenge's window verbatim rather than extending it", () => {
    const id = newIdentity();
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
    expect(resp.issued_at).toBe(req.issued_at);
    expect(resp.expires_at).toBe(req.expires_at);
  });

  it("spends a nonce exactly once", async () => {
    const id = newIdentity();
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
    await expect(v.verifyResponse(resp)).resolves.toBeTruthy();
    await expect(v.verifyResponse(resp)).rejects.toThrow(/nonce already used/);
  });

  it("rejects an approval collected at another origin", async () => {
    const id = newIdentity();
    // The user really did approve — at evil.example. The signature is valid.
    const evil = verifierFor(id.document, "https://evil.example");
    const req = evil.newRequest({ statement: "Sign in" });
    const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
    await expect(evil.verifyResponse(resp)).resolves.toBeTruthy();

    const real = verifierFor(id.document, RP_ORIGIN);
    await expect(real.verifyResponse(resp)).rejects.toThrow(/bound to https:\/\/evil.example/);
  });

  it("rejects an approval signed by a different key", async () => {
    const id = newIdentity();
    const other = newIdentity();
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, { identity: WHO, privateKey: other.privateKey, nowMs: NOW });
    await expect(v.verifyResponse(resp)).rejects.toThrow(/signature/);
  });

  const tamper: [string, (r: SignInResponse) => void][] = [
    ["statement", (r) => { r.statement = "Sign in and send all my money"; }],
    ["action", (r) => { r.action = "link"; }],
    ["scopes", (r) => { r.scopes = ["dav:rw:apps/net.poweur.guestbook"]; }],
    ["identity", (r) => { r.identity = "mallory.poweur.net"; }],
    ["request_id", (r) => { r.request_id = "req_other"; }],
    ["key_id", (r) => { r.key_id = "identity "; }],
  ];
  for (const [field, mutate] of tamper) {
    it(`rejects an approval whose ${field} was edited after signing`, async () => {
      const id = newIdentity();
      const v = verifierFor(id.document);
      const req = v.newRequest({ statement: "Sign in to the Guestbook" });
      const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
      mutate(resp);
      await expect(v.verifyResponse(resp)).rejects.toThrow();
    });
  }

  it("rejects a malformed signature outright", async () => {
    const id = newIdentity();
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
    resp.signature = "not-a-signature";
    await expect(v.verifyResponse(resp)).rejects.toThrow();
  });

  it("rejects a response whose scopes are not in canonical order", async () => {
    const id = newIdentity();
    const v = verifierFor(id.document);
    const req = v.newRequest({ scopes: ["profile:read", "dav:rw:apps/net.poweur.guestbook"] });
    const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
    resp.scopes = ["profile:read", "dav:rw:apps/net.poweur.guestbook"];
    await expect(v.verifyResponse(resp)).rejects.toThrow(/canonical form/);
  });

  it("fails closed when the nonce cache cannot answer", async () => {
    const id = newIdentity();
    const v = new SignInVerifier({
      origin: RP_ORIGIN,
      now: () => NOW,
      resolve: async () => ({ document: id.document, source: "web" }),
      nonces: { use: () => { throw new Error("redis down"); } },
    });
    const req = v.newRequest({});
    const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
    await expect(v.verifyResponse(resp)).rejects.toThrow(/nonce cache unavailable/);
  });

  it("rejects an unresolvable identity without having spent anything else", async () => {
    const v = new SignInVerifier({
      origin: RP_ORIGIN,
      now: () => NOW,
      resolve: async () => { throw new Error("NXDOMAIN"); },
    });
    const id = newIdentity();
    const req = v.newRequest({});
    const resp = signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, nowMs: NOW });
    await expect(v.verifyResponse(resp)).rejects.toThrow(/NXDOMAIN/);
  });

  it("verifies against a retired key still inside its rotation grace window", async () => {
    const old = newIdentity();
    const fresh = newIdentity();
    const doc: IdentityDocument = {
      ...fresh.document,
      previous_keys: [
        { public_key: old.document.public_key, valid_until: "2026-01-15T12:00:00Z" },
      ],
    };
    const v = verifierFor(doc);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, { identity: WHO, privateKey: old.privateKey, nowMs: NOW });
    await expect(v.verifyResponse(resp)).resolves.toBeTruthy();
  });

  it("rejects a key whose grace window has closed", async () => {
    const old = newIdentity();
    const fresh = newIdentity();
    const doc: IdentityDocument = {
      ...fresh.document,
      previous_keys: [
        { public_key: old.document.public_key, valid_until: "2026-01-14T00:00:00Z" },
      ],
    };
    const v = verifierFor(doc);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, { identity: WHO, privateKey: old.privateKey, nowMs: NOW });
    await expect(v.verifyResponse(resp)).rejects.toThrow();
  });
});

describe("session delegation", () => {
  function delegate(identityPrivateKey: Uint8Array, opts: { expiresAt?: string; issuedAt?: string } = {}) {
    const session = generateSigningKeypair();
    const sessionPub = toBase64url(session.publicKey);
    const issued = opts.issuedAt ?? "2026-01-15T08:00:00Z";
    const expires = opts.expiresAt ?? "2026-01-15T20:00:00Z";
    const proof = {
      session_public_key: sessionPub,
      issued_at: issued,
      expires_at: expires,
      nonce: "c2Vzc2lvbi1ub25jZQ",
      identity_signature: signCanonical(
        identityPrivateKey,
        canonicalSessionRegistration(WHO, sessionPub, issued, expires, "c2Vzc2lvbi1ub25jZQ"),
      ),
    };
    return { session, proof };
  }

  it("accepts an approval signed by a delegated session key", async () => {
    const id = newIdentity();
    const { session, proof } = delegate(id.privateKey);
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, {
      identity: WHO,
      privateKey: session.privateKey,
      sessionId: "sess_1",
      sessionProof: proof,
      nowMs: NOW,
    });
    const result = await v.verifyResponse(resp);
    expect(result.sessionDelegated).toBe(true);
    expect(result.keyId).toBe("session:sess_1");
  });

  it("rejects a proof the identity key did not sign", async () => {
    const id = newIdentity();
    const impostor = newIdentity();
    const { session, proof } = delegate(impostor.privateKey);
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, {
      identity: WHO, privateKey: session.privateKey, sessionId: "s", sessionProof: proof, nowMs: NOW,
    });
    await expect(v.verifyResponse(resp)).rejects.toThrow();
  });

  it("rejects an expired session proof", async () => {
    const id = newIdentity();
    const { session, proof } = delegate(id.privateKey, {
      issuedAt: "2026-01-14T08:00:00Z",
      expiresAt: "2026-01-14T20:00:00Z",
    });
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, {
      identity: WHO, privateKey: session.privateKey, sessionId: "s", sessionProof: proof, nowMs: NOW,
    });
    await expect(v.verifyResponse(resp)).rejects.toThrow();
  });

  it("rejects a proof claiming more than the 24 hour session cap", async () => {
    const id = newIdentity();
    const { session, proof } = delegate(id.privateKey, {
      issuedAt: "2026-01-15T08:00:00Z",
      expiresAt: "2026-01-20T08:00:00Z",
    });
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    const resp = signSignInRequest(req, {
      identity: WHO, privateKey: session.privateKey, sessionId: "s", sessionProof: proof, nowMs: NOW,
    });
    await expect(v.verifyResponse(resp)).rejects.toThrow();
  });

  it("insists that key_id and session_proof agree", async () => {
    const id = newIdentity();
    const { session, proof } = delegate(id.privateKey);
    const v = verifierFor(id.document);
    const req = v.newRequest({});

    // key_id names a session, no proof attached.
    const noProof = signSignInRequest(req, {
      identity: WHO, privateKey: session.privateKey, sessionId: "s", sessionProof: proof, nowMs: NOW,
    });
    delete noProof.session_proof;
    await expect(v.verifyResponse(noProof)).rejects.toThrow(/no session_proof/);

    // Proof attached, key_id says the identity key signed. A fresh challenge:
    // the first presentation above already spent that nonce, which is itself
    // the rule — a rejected approval does not get a second run at the cache.
    const identitySigned = signSignInRequest(v.newRequest({}), {
      identity: WHO, privateKey: id.privateKey, nowMs: NOW,
    });
    identitySigned.session_proof = proof;
    await expect(v.verifyResponse(identitySigned)).rejects.toThrow(/key_id is/);
  });

  it("rejects an incomplete proof", () => {
    const id = newIdentity();
    const { proof } = delegate(id.privateKey);
    for (const field of ["session_public_key", "issued_at", "expires_at", "nonce", "identity_signature"] as const) {
      const broken = { ...proof, [field]: "" };
      expect(() =>
        verifySessionProof(new Uint8Array(32), WHO, broken, NOW),
      ).toThrow();
    }
  });

  it("refuses to sign with half a delegation", () => {
    const id = newIdentity();
    const { proof } = delegate(id.privateKey);
    const v = verifierFor(id.document);
    const req = v.newRequest({});
    expect(() =>
      signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, sessionId: "s", nowMs: NOW }),
    ).toThrow();
    expect(() =>
      signSignInRequest(req, { identity: WHO, privateKey: id.privateKey, sessionProof: proof, nowMs: NOW }),
    ).toThrow();
  });
});

// ── Nonce cache ──────────────────────────────────────────────────────────────

describe("MemoryNonceCache", () => {
  it("claims a key once", () => {
    let now = NOW;
    const cache = new MemoryNonceCache(() => now);
    expect(cache.use("k", NOW + 60_000)).toBe(true);
    expect(cache.use("k", NOW + 60_000)).toBe(false);
    expect(cache.use("other", NOW + 60_000)).toBe(true);
  });

  it("forgets entries once the response they guarded would have expired", () => {
    let now = NOW;
    const cache = new MemoryNonceCache(() => now);
    cache.use("k", NOW + 60_000);
    expect(cache.size).toBe(1);
    now = NOW + 120_000;
    // The pruning pass runs on the next use, which keeps the map bounded by
    // the request rate over one window without a timer.
    expect(cache.use("k", now + 60_000)).toBe(true);
    expect(cache.size).toBe(1);
  });
});

// ── Relying-party metadata ───────────────────────────────────────────────────

const META: RelyingPartyMetadata = {
  poweur_auth: "1",
  origin: RP_ORIGIN,
  name: "Poweur Guestbook",
  app_id: "net.poweur.guestbook",
  response_uris: [`${RP_ORIGIN}/auth/callback`],
  scopes: ["profile:read", "dav:rw:apps/net.poweur.guestbook"],
};

describe("relying-party metadata", () => {
  it("accepts a well-formed document served from its own origin", () => {
    expect(() => validateRelyingPartyMetadata(META, RP_ORIGIN)).not.toThrow();
  });

  const bad: [string, Partial<RelyingPartyMetadata>][] = [
    ["a version we do not speak", { poweur_auth: "2" }],
    ["no name", { name: "  " }],
    ["an origin other than where it was served", { origin: "https://evil.example" }],
    ["an off-origin response_uri", { response_uris: ["https://evil.example/cb"] }],
    ["an off-origin logo", { logo_uri: "https://cdn.evil.example/logo.png" }],
    ["an off-origin poll_uri", { poll_uri: "https://evil.example/poll" }],
    ["an app_id that is not its own namespace", { app_id: "net.poweur.mail" }],
    ["a scope outside its namespace", { scopes: ["dav:rw:apps/net.poweur.mail"] }],
  ];
  for (const [what, patch] of bad) {
    it(`rejects metadata with ${what}`, () => {
      expect(() => validateRelyingPartyMetadata({ ...META, ...patch }, RP_ORIGIN)).toThrow();
    });
  }

  it("allows only published response_uris once a list exists", () => {
    expect(metadataAllowsResponseUri(META, `${RP_ORIGIN}/auth/callback`)).toBe(true);
    expect(metadataAllowsResponseUri(META, `${RP_ORIGIN}/auth/callback/`)).toBe(true);
    // Same-origin but unpublished: this is the open-redirect containment.
    expect(metadataAllowsResponseUri(META, `${RP_ORIGIN}/redirect?to=evil`)).toBe(false);
    expect(metadataAllowsResponseUri(META, "https://evil.example/cb")).toBe(false);
    // No list published: any same-origin URI, the permissive demo default.
    expect(metadataAllowsResponseUri({ ...META, response_uris: [] }, `${RP_ORIGIN}/x`)).toBe(true);
  });

  it("checks a request against the RP's own document", () => {
    const req: SignInRequest = {
      poweur_auth: "1",
      request_id: "r",
      audience: RP_ORIGIN,
      nonce: "n",
      issued_at: "2026-01-15T09:29:00Z",
      expires_at: "2026-01-15T09:31:00Z",
      action: "signin",
      response_uri: `${RP_ORIGIN}/auth/callback`,
    };
    expect(() => checkRequestAgainstMetadata(req, META)).not.toThrow();
    expect(() =>
      checkRequestAgainstMetadata({ ...req, response_uri: `${RP_ORIGIN}/elsewhere` }, META),
    ).toThrow();
  });

  it("fetches metadata over an injected fetch and refuses redirects", async () => {
    const calls: RequestInit[] = [];
    const meta = await fetchRelyingPartyMetadata(RP_ORIGIN, {
      fetch: (async (_url: string, init: RequestInit) => {
        calls.push(init);
        return new Response(JSON.stringify(META), { status: 200 });
      }) as unknown as typeof globalThis.fetch,
    });
    expect(meta.name).toBe("Poweur Guestbook");
    expect(calls[0]?.redirect).toBe("error");
  });

  it("surfaces a non-200 and a body that is not the RP's own document", async () => {
    const respond = (body: string, status = 200) =>
      fetchRelyingPartyMetadata(RP_ORIGIN, {
        fetch: (async () => new Response(body, { status })) as unknown as typeof globalThis.fetch,
      });
    await expect(respond("", 404)).rejects.toThrow(/404/);
    await expect(respond("<html>")).rejects.toThrow(/not JSON/);
    await expect(respond(JSON.stringify({ ...META, origin: "https://evil.example" }))).rejects.toThrow();
  });
});

// ── Consent rendering ────────────────────────────────────────────────────────

describe("consent rendering", () => {
  it("describes every scope as a sentence, never a raw token", () => {
    expect(describeScope("profile:read", "Guestbook")).toContain("public profile");
    expect(describeScope("messages:send", "Guestbook")).toContain("send messages");
    expect(describeScope("dav:rw:apps/net.poweur.guestbook", "Guestbook")).toContain(
      "read and write files in /apps/net.poweur.guestbook",
    );
    expect(describeScope("dav:read:apps/net.poweur.guestbook", "Guestbook")).toContain("cannot write");
  });

  it("tells the user not to approve something it cannot explain", () => {
    expect(describeScope("root:everything", "Guestbook")).toMatch(/do not approve/);
  });

  it("headlines the action", () => {
    const req = { audience: RP_ORIGIN, domain: "guestbook.poweur.net", action: "signin" } as SignInRequest;
    expect(summarizeSignInRequest(req, "Guestbook")).toBe(
      "Sign in to Guestbook (guestbook.poweur.net) with your Poweur ID",
    );
    expect(summarizeSignInRequest({ ...req, action: "signup" }, "")).toContain("Create an account");
    expect(summarizeSignInRequest({ ...req, action: "link" }, "")).toContain("Link your Poweur ID");
  });
});

describe("canonical string", () => {
  it("puts every decided-on field inside the signed bytes", () => {
    const canonical = canonicalSignInResponse({
      version: "1",
      requestId: "req_1",
      identity: "Alice.Poweur.NET",
      audience: RP_ORIGIN,
      nonce: "n",
      issuedAt: "2026-01-15T09:29:00Z",
      expiresAt: "2026-01-15T09:31:00Z",
      action: "signin",
      statement: "Sign in",
      scopes: ["dav:rw:apps/net.poweur.guestbook", "profile:read"],
      keyId: "identity",
    });
    expect(canonical.split("\n")).toEqual([
      "poweur-signin",
      "1",
      "req_1",
      "alice.poweur.net",
      RP_ORIGIN,
      "n",
      "2026-01-15T09:29:00Z",
      "2026-01-15T09:31:00Z",
      "signin",
      "Sign in",
      "dav:rw:apps/net.poweur.guestbook,profile:read",
      "identity",
    ]);
  });
});
