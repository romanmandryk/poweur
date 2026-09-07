/**
 * Resolver unit tests. The interesting cases here are all failures — the
 * resolver's job is to fail closed, and these pin every way it must.
 */

import { describe, expect, it } from "vitest";

import { signDocumentWithKey } from "../src/document.js";
import { generateSigningKeypair } from "../src/crypto/index.js";
import { toBase64url } from "../src/encoding.js";
import { ResolveCache, resolveEncryptionKey, resolveIdentity, resolveSigningKey } from "../src/resolve.js";
import type { IdentityDocument } from "../src/types.js";

const IDENTITY = "alice.example.org";

function signedDocument(overrides: Partial<IdentityDocument> = {}) {
  const { privateKey, publicKey } = generateSigningKeypair();
  const base: IdentityDocument = {
    version: 1,
    identity: IDENTITY,
    public_key: `ed25519:${toBase64url(publicKey)}`,
    encryption_public_key: `x25519:${toBase64url(new Uint8Array(32).fill(7))}`,
    relay: "relay.example.org",
    capabilities: ["messaging"],
    updated_at: "2026-01-15T09:30:00Z",
    ...overrides,
  };
  return { document: signDocumentWithKey(base, privateKey), privateKey, publicKey };
}

/** A fetch that serves one well-known document and 404s everything else. */
function webFetch(document: IdentityDocument | null, status = 200): typeof globalThis.fetch {
  return (async () =>
    document
      ? new Response(JSON.stringify(document), { status })
      : new Response("not found", { status: 404 })) as unknown as typeof globalThis.fetch;
}

function txt(records: Record<string, string[]>) {
  return {
    async lookupTxt(name: string): Promise<string[]> {
      const found = records[name];
      if (!found) throw new Error(`no TXT records for ${name}`);
      return found;
    },
  };
}

describe("web resolution", () => {
  it("returns a verified document from the well-known path", async () => {
    const { document } = signedDocument();
    const result = await resolveIdentity(IDENTITY, { fetch: webFetch(document), skipDns: true });
    expect(result.source).toBe("web");
    expect(result.document.identity).toBe(IDENTITY);
  });

  it("rejects a document whose signature does not verify", async () => {
    const { document } = signedDocument();
    const tampered = { ...document, relay: "evil.example.org" };
    await expect(
      resolveIdentity(IDENTITY, { fetch: webFetch(tampered), skipDns: true }),
    ).rejects.toThrow(/not found via web/);
  });

  it("rejects a document served for the wrong identity", async () => {
    const { document } = signedDocument({ identity: "mallory.example.org" });
    await expect(
      resolveIdentity(IDENTITY, { fetch: webFetch(document), skipDns: true }),
    ).rejects.toMatchObject({ code: "key_mismatch" });
  });

  it("rejects a document larger than the 16KB cap", async () => {
    const fetchImpl = (async () =>
      new Response("x".repeat(17 * 1024), { status: 200 })) as unknown as typeof globalThis.fetch;
    await expect(
      resolveIdentity(IDENTITY, { fetch: fetchImpl, skipDns: true }),
    ).rejects.toThrow(/not found via web|exceeds 16KB/);
  });

  it("refuses private and loopback targets unless explicitly allowed", async () => {
    const { document } = signedDocument({ identity: "alice.localhost" });
    await expect(
      resolveIdentity("alice.localhost", { fetch: webFetch(document), skipDns: true }),
    ).rejects.toThrow(/private\/loopback/);
  });

  it("allows private targets when the test flag is set", async () => {
    const { document } = signedDocument({ identity: "alice.localhost" });
    const result = await resolveIdentity("alice.localhost", {
      fetch: webFetch(document),
      skipDns: true,
      allowPrivate: true,
    });
    expect(result.document.identity).toBe("alice.localhost");
  });

  it("refuses an invalid identity name before making any request", async () => {
    let called = false;
    const fetchImpl = (async () => {
      called = true;
      return new Response("{}", { status: 200 });
    }) as unknown as typeof globalThis.fetch;
    await expect(resolveIdentity("nodots", { fetch: fetchImpl })).rejects.toThrow(/FQDN/);
    expect(called).toBe(false);
  });
});

describe("DNS resolution", () => {
  it("builds a document from TXT records when the web path fails", async () => {
    const { publicKey } = generateSigningKeypair();
    const bare = toBase64url(publicKey);
    const result = await resolveIdentity(IDENTITY, {
      fetch: webFetch(null),
      txt: txt({
        [`_poweur.${IDENTITY}`]: [`poweur-pubkey=${bare}`],
        [`_poweur-enc.${IDENTITY}`]: [`poweur-enckey=${toBase64url(new Uint8Array(32))}`],
      }),
    });
    expect(result.source).toBe("dns");
    expect(result.document.public_key).toBe(`ed25519:${bare}`);
    expect(result.document.encryption_public_key?.startsWith("x25519:")).toBe(true);
  });

  it("reports both failures when neither source answers", async () => {
    await expect(
      resolveIdentity(IDENTITY, { fetch: webFetch(null), txt: txt({}) }),
    ).rejects.toMatchObject({ code: "resolve_failed" });
  });

  it("fails closed when web and DNS disagree about the key", async () => {
    const { document } = signedDocument();
    const other = generateSigningKeypair();
    await expect(
      resolveIdentity(IDENTITY, {
        fetch: webFetch(document),
        txt: txt({ [`_poweur.${IDENTITY}`]: [`poweur-pubkey=${toBase64url(other.publicKey)}`] }),
      }),
    ).rejects.toMatchObject({ code: "key_mismatch" });
  });

  it("reports source=both when web and DNS agree", async () => {
    const { document, publicKey } = signedDocument();
    const result = await resolveIdentity(IDENTITY, {
      fetch: webFetch(document),
      txt: txt({ [`_poweur.${IDENTITY}`]: [`poweur-pubkey=${toBase64url(publicKey)}`] }),
    });
    expect(result.source).toBe("both");
  });
});

describe("caching and key helpers", () => {
  it("serves a cached result without re-fetching", async () => {
    const { document } = signedDocument();
    let calls = 0;
    const fetchImpl = (async () => {
      calls++;
      return new Response(JSON.stringify(document), { status: 200 });
    }) as unknown as typeof globalThis.fetch;
    const cache = new ResolveCache();
    await resolveIdentity(IDENTITY, { fetch: fetchImpl, skipDns: true, cache });
    await resolveIdentity(IDENTITY, { fetch: fetchImpl, skipDns: true, cache });
    expect(calls).toBe(1);
  });

  it("returns bare keys, prefix stripped", async () => {
    const { document, publicKey } = signedDocument();
    const options = { fetch: webFetch(document), skipDns: true };
    expect(await resolveSigningKey(IDENTITY, options)).toBe(toBase64url(publicKey));
    expect(await resolveEncryptionKey(IDENTITY, options)).toBe(toBase64url(new Uint8Array(32).fill(7)));
  });

  it("returns null rather than throwing when a key cannot be resolved", async () => {
    const options = { fetch: webFetch(null), skipDns: true };
    expect(await resolveSigningKey(IDENTITY, options)).toBeNull();
    expect(await resolveEncryptionKey(IDENTITY, options)).toBeNull();
  });
});
