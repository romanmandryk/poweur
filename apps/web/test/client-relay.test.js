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

import "./helpers/browser-globals.mjs";
import { resetWebStorage } from "./helpers/browser-globals.mjs";
import { startRelay } from "./helpers/relay.mjs";
import { createWebIdentity, unlock } from "./helpers/identity.mjs";

import { clientFor, identityApiFor, lookup } from "../js/client.js";
import { loadIdentityRecord, saveIdentityRecord, relayUrlFor, loadSessionRecord } from "../js/storage.js";
import { unwrapKeysWithPin } from "../js/vault.js";

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

  it("reopens the wrapped keys with the right PIN only", async () => {
    const { encryptedKeys } = loadIdentityRecord(alice.identity);
    const opened = await unwrapKeysWithPin("test-pin", encryptedKeys);
    expect(opened.signingJWK).toEqual(alice.signingJWK);
    await expect(unwrapKeysWithPin("wrong-pin", encryptedKeys)).rejects.toThrow();
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
          encryptedKeys: { kdf: "pbkdf2" },
          supportsPRF: false,
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

    it("mints a DAV token and runs the file browser's lifecycle", async () => {
      const dav = await clientFor(alice.identity).dav();

      const roots = (await dav.list("")).map((e) => e.name);
      expect(roots).toEqual(expect.arrayContaining(["public", "private", "shared", "apps"]));

      await dav.mkdir("private/notes");
      await dav.write("private/notes/hello.txt", "from the web client");
      expect(await dav.readText("private/notes/hello.txt")).toBe("from the web client");

      const listed = await dav.list("private/notes");
      expect(listed.map((e) => e.name)).toEqual(["hello.txt"]);
      expect(listed[0].size).toBeGreaterThan(0);

      await dav.move("private/notes/hello.txt", "private/notes/renamed.txt");
      expect((await dav.list("private/notes")).map((e) => e.name)).toEqual(["renamed.txt"]);

      const quota = await dav.quota();
      expect(quota.used_bytes).toBeGreaterThan(0);

      await dav.remove("private/notes/renamed.txt");
      expect(await dav.list("private/notes")).toEqual([]);
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
