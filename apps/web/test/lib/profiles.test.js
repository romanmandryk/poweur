/**
 * @vitest-environment happy-dom
 *
 * The profile resolver (E15-T1) — how the app answers "who is this?" rather
 * than "what is their key?".
 *
 * The behaviour that matters is the degradation: profile.json is served from
 * the identity's own Host, which a browser cannot fake against a shared dev
 * relay, so the resolver must still return something useful from the identity
 * document alone.
 */
import { describe, it, expect, beforeAll, beforeEach, vi, afterEach } from "vitest";
import {
  generateIdentityKeys, signerFor, signDocument, newDocument, toBase64url, crypto as sdk,
} from "@poweur/client";

import { resolveProfile, clearProfileCache, cachedProfile, avatarUrl } from "../../src/lib/profiles.js";

/**
 * A genuinely signed document — the resolver verifies signatures, so an
 * invented one would only ever exercise the failure path.
 */
let DOCUMENT;

beforeAll(async () => {
  const keys = generateIdentityKeys("alice.poweur.net");
  const { signer } = signerFor(keys);
  DOCUMENT = await signDocument(
    newDocument({
      identity: "alice.poweur.net",
      publicKey: toBase64url(sdk.ed25519PublicKey(keys.signingPrivateKey)),
      encryptionPublicKey: toBase64url(sdk.x25519PublicKey(keys.encryptionPrivateKey)),
      relay: "poweur.net",
      capabilities: ["messaging", "files"],
      updatedAt: "2026-01-01T00:00:00Z",
    }),
    signer,
  );
});

/** Serve the well-known routes this identity would expose, per URL. */
function serveWellKnown(routes) {
  return vi.fn(async (url) => {
    const path = new URL(url).pathname;
    if (!(path in routes)) return new Response("not found", { status: 404 });
    return new Response(JSON.stringify(routes[path]), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
}

const realFetch = globalThis.fetch;

beforeEach(() => {
  localStorage.clear();
  clearProfileCache();
});

afterEach(() => {
  globalThis.fetch = realFetch;
});

describe("resolveProfile", () => {
  it("returns display name, bio, links and avatar when profile.json is reachable", async () => {
    globalThis.fetch = serveWellKnown({
      "/.well-known/poweur/id.json": DOCUMENT,
      "/.well-known/poweur/profile.json": {
        version: 1,
        display_name: "Alice Example",
        bio: "Builds things",
        avatar: "avatar.png",
        links: [{ label: "site", url: "https://alice.example" }],
      },
      "/.well-known/poweur/capabilities.json": { version: 1, features: { messaging: "1", files: "2" } },
    });

    const entry = await resolveProfile("alice.poweur.net", "https://poweur.net");

    expect(entry.displayName).toBe("Alice Example");
    expect(entry.bio).toBe("Builds things");
    expect(entry.links).toEqual([{ label: "site", url: "https://alice.example" }]);
    expect(entry.avatar).toBe("https://alice.poweur.net/.well-known/poweur/avatar.png");
    expect(entry.capabilities.features).toMatchObject({ files: "2" });
  });

  it("falls back to the identity document when profile.json is unreachable", async () => {
    // The normal case on a shared dev relay: id.json resolves, the Host-routed
    // presentation files do not.
    globalThis.fetch = serveWellKnown({ "/.well-known/poweur/id.json": DOCUMENT });

    const entry = await resolveProfile("alice.poweur.net", "https://poweur.net");

    expect(entry.profile).toBeNull();
    expect(entry.displayName).toBeNull();
    expect(entry.document.identity).toBe("alice.poweur.net");
    // Capabilities still come through — the document carries them.
    expect(Object.keys(entry.capabilities.features).sort()).toEqual(["files", "messaging"]);
  });

  it("throws only when the identity itself cannot be resolved", async () => {
    globalThis.fetch = serveWellKnown({});
    await expect(resolveProfile("ghost.poweur.net", "https://poweur.net")).rejects.toThrow(/not found/i);
  });

  it("caches by identity and coalesces concurrent lookups into one fetch", async () => {
    const fetchSpy = serveWellKnown({ "/.well-known/poweur/id.json": DOCUMENT });
    globalThis.fetch = fetchSpy;

    const [a, b] = await Promise.all([
      resolveProfile("alice.poweur.net", "https://poweur.net"),
      resolveProfile("alice.poweur.net", "https://poweur.net"),
    ]);
    expect(a).toBe(b);

    const afterConcurrent = fetchSpy.mock.calls.length;
    await resolveProfile("alice.poweur.net", "https://poweur.net");
    expect(fetchSpy.mock.calls.length).toBe(afterConcurrent); // served from cache

    expect(cachedProfile("alice.poweur.net")).toBe(a);
    expect(cachedProfile("ALICE.POWEUR.NET")).toBe(a); // identity is case-insensitive
    clearProfileCache();
    expect(cachedProfile("alice.poweur.net")).toBeNull();
  });
});

describe("avatarUrl", () => {
  it("maps an avatar file name to the identity's public route", () => {
    expect(avatarUrl("alice.poweur.net", "avatar-1a2b3c4d.png"))
      .toBe("https://alice.poweur.net/.well-known/poweur/avatar-1a2b3c4d.png");
  });

  it("refuses anything that is not a flat image file name", () => {
    // The avatar is a file name, never an external URL or a path — so a
    // profile cannot point the app at an arbitrary origin.
    expect(avatarUrl("alice.poweur.net", "https://evil.example/x.png")).toBeNull();
    expect(avatarUrl("alice.poweur.net", "public/avatar.png")).toBeNull();
    expect(avatarUrl("alice.poweur.net", "profile.json")).toBeNull();
    expect(avatarUrl("alice.poweur.net", "")).toBeNull();
    expect(avatarUrl("alice.poweur.net", null)).toBeNull();
  });
});
