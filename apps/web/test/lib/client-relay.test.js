/**
 * The web client against a real Go relay.
 *
 * After E15-T6 the protocol is `@poweur/client`'s and is tested in
 * `packages/client-ts`. What `apps/web` still owns — and what this suite
 * exists for — is the seam: WebCrypto-held keys, `localStorage` identity
 * records, and a relay URL taken from the record rather than the origin. Those
 * only fail against a real server, so this spawns one.
 */
import { describe, it, expect, beforeAll, afterAll, beforeEach } from "vitest";

import "../helpers/browser-globals.mjs";
import { resetWebStorage } from "../helpers/browser-globals.mjs";
import { startRelay } from "../helpers/relay.mjs";
import { createWebIdentity, unlock } from "../helpers/identity.mjs";

import { clientFor, identityApiFor, lookup } from "../../src/lib/client.js";
import { loadIdentityRecord, saveIdentityRecord, relayUrlFor, loadSessionRecord } from "../../src/lib/storage.js";
import { unwrapKeysAES } from "../../src/lib/vault.js";

describe("web client ↔ real relay", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;
  let alice;
  let bob;

  beforeAll(async () => {
    relay = await startRelay();
    const suffix = Date.now().toString(36);
    alice = await createWebIdentity(relay.baseUrl, `webalice${suffix}.poweur.net`);
    bob = await createWebIdentity(relay.baseUrl, `webbob${suffix}.poweur.net`);
  }, 120_000);

  afterAll(() => relay?.stop());

  it("registers with a WebCrypto-held key the relay accepts", async () => {
    const response = await identityApiFor(relay.baseUrl).get(alice.identity);
    expect(response.identity).toBe(alice.identity);
    expect(response.public_key).toContain(alice.publicKey);
  });

  it("stores the identity record with its own relay, wrapped keys and no plaintext key", () => {
    const record = loadIdentityRecord(alice.identity);
    expect(record.relay).toBe(relay.baseUrl);
    expect(relayUrlFor(alice.identity)).toBe(relay.baseUrl);
    expect(JSON.stringify(record)).not.toContain(alice.signingJWK.d);
  });

  it("reopens the wrapped keys with the wrap secret only", async () => {
    const { encryptedKeys } = loadIdentityRecord(alice.identity);
    const opened = await unwrapKeysAES(alice.wrapSecret, encryptedKeys);
    expect(opened.signingJWK).toEqual(alice.signingJWK);
    await expect(unwrapKeysAES(crypto.getRandomValues(new Uint8Array(32)), encryptedKeys)).rejects.toThrow();
  });

  it("resolves an identity web-first through the relay", async () => {
    const { document, source } = await lookup(bob.identity, relay.baseUrl);
    expect(document.identity).toBe(bob.identity);
    expect(document.encryption_public_key).toContain(bob.encPublicKey);
    expect(["web", "both"]).toContain(source);
  });

  it("returns no client while the identity is locked", () => {
    resetWebStorage();
    expect(clientFor(alice.identity)).toBeNull();
  });

  describe("unlocked", () => {
    beforeEach(() => {
      // resetWebStorage() above clears the records too; put them back.
      for (const who of [alice, bob]) {
        saveIdentityRecord(who.identity, {
          identity: who.identity,
          publicKey: who.publicKey,
          encPublicKey: who.encPublicKey,
          relay: relay.baseUrl,
          encryptedKeys: { kdf: "prf" },
          supportsPRF: true,
          createdAt: new Date().toISOString(),
        });
      }
      unlock(alice);
    });

    it("registers a session and reuses it", async () => {
      const client = clientFor(alice.identity);
      const first = await client.sessions.ensure(client.signer);
      expect(first.sessionId).toBeTruthy();
      expect(first.relayUrl).toBe(relay.baseUrl);
      // Persisted to sessionStorage by BrowserSessionStore.
      expect(loadSessionRecord(alice.identity).sessionId).toBe(first.sessionId);

      const second = await clientFor(alice.identity).sessions.ensure(client.signer);
      expect(second.sessionId).toBe(first.sessionId);
    });

    it("sends session-signed and identity-signed messages that Bob can decrypt", async () => {
      const client = clientFor(alice.identity);
      await client.sessions.ensure(client.signer);

      for (const signWith of ["session", "identity"]) {
        const secret = `hello-${signWith}-${Date.now()}`;
        const { message } = await client.send(bob.identity, secret, { signWith });
        expect(message.payload).not.toBe(secret);
        expect(Boolean(message.session_id)).toBe(signWith === "session");
      }

      unlock(bob);
      const { messages } = await clientFor(bob.identity).inbox();
      const plaintexts = messages.map((m) => m.plaintext);
      expect(plaintexts.some((p) => p?.startsWith("hello-session-"))).toBe(true);
      expect(plaintexts.some((p) => p?.startsWith("hello-identity-"))).toBe(true);
    });

    it("writes and reads its system files through the owner API", async () => {
      const client = clientFor(alice.identity);
      const files = client.system();
      expect(await files.readOptional(".poweur/relay/inbox-policy.json")).toBeNull();

      await client.setPolicy("contacts_only");
      expect((await client.policy()).policy.mode).toBe("contacts_only");

      // A tiny PNG: an avatar is written to .poweur/public and served publicly.
      const png = Uint8Array.from(atob("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="), (c) => c.charCodeAt(0));
      await files.write(".poweur/public/avatar-test.png", png);
      const got = await files.get(".poweur/public/avatar-test.png");
      expect(got.bytes).toEqual(png);
      expect(await files.remove(".poweur/public/avatar-test.png")).toBe(true);

      // The relay's own zone is read-only to the owner.
      await expect(files.write(".poweur/state/devices.json", "{}")).rejects.toThrow();
      expect((await client.devices().list()).devices).toEqual(expect.any(Array));
    });

    it("revokes a session", async () => {
      const client = clientFor(alice.identity);
      await client.sessions.ensure(client.signer);
      const { relayRevoked } = await client.sessions.revoke(client.signer);
      expect(relayRevoked).toBe(true);
      expect(loadSessionRecord(alice.identity)).toBeNull();
    });
  });
});
