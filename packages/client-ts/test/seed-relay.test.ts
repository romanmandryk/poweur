/**
 * Seed-derived identities against a real Go relay (EPIC-011 E11-T1).
 *
 * Unit conformance proves TypeScript and Go derive the same bytes; this file
 * proves those bytes make a *working identity*: it registers, resolves,
 * signs, sends and decrypts exactly like a randomly generated one. The last
 * test is the recovery guarantee itself — throw the client away, keep only 32
 * bytes, and the identity comes back able to read its own inbox.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { PoweurClient } from "../src/client.js";
import { identityKeysFromSeed, signerFor } from "../src/crypto/keys.js";
import { newSeed } from "../src/crypto/seed.js";
import { toBase64url } from "../src/encoding.js";
import { verifyDocument } from "../src/document.js";
import { RelayClient } from "../src/http.js";
import { createIdentity, IdentityApi } from "../src/identity.js";
import { resolveIdentity } from "../src/resolve.js";
import { MemorySessionStore } from "../src/session.js";
import { localResolveOptions, uniqueIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

interface SeedIdentity {
  identity: string;
  seed: Uint8Array;
  client: PoweurClient;
}

describe("seed-derived identities ↔ real relay", () => {
  let relay: RunningRelay;

  /** Build a client for `identity` from `seed` alone — no stored key material. */
  function clientFromSeed(identity: string, seed: Uint8Array): PoweurClient {
    const { signer, decryptor } = signerFor(identityKeysFromSeed(identity, seed));
    return new PoweurClient({
      relayUrl: relay.baseUrl,
      signer,
      decryptor,
      sessionStore: new MemorySessionStore(),
      resolve: localResolveOptions(relay.baseUrl),
    });
  }

  async function registerFromSeed(prefix: string): Promise<SeedIdentity> {
    const identity = uniqueIdentity(prefix);
    const seed = newSeed();
    const api = new IdentityApi(new RelayClient(relay.baseUrl));
    await createIdentity(api, identity, {
      hosted: true,
      keys: identityKeysFromSeed(identity, seed),
    });
    return { identity, seed, client: clientFromSeed(identity, seed) };
  }

  let alice: SeedIdentity;
  let bob: SeedIdentity;

  beforeAll(async () => {
    relay = await startRelay();
    alice = await registerFromSeed("seedalice");
    bob = await registerFromSeed("seedbob");
  }, 180_000);

  afterAll(() => relay?.stop());

  it("registers a seed-derived identity the relay accepts and serves", async () => {
    const result = await resolveIdentity(alice.identity, localResolveOptions(relay.baseUrl));
    expect(result.document.identity).toBe(alice.identity);
    expect(result.document.public_key).toBe(alice.client.signer.publicKey);
    // Self-signed: the relay stores the document but never signs it.
    expect(() => verifyDocument(result.document)).not.toThrow();
  });

  it("sends and decrypts between two seed-derived identities", async () => {
    await alice.client.send(bob.identity, "derived from a seed");
    const { messages } = await bob.client.inbox();
    expect(messages.map((m) => m.plaintext)).toContain("derived from a seed");
  });

  it("interoperates with a randomly generated identity in both directions", async () => {
    const carol = await (async () => {
      const identity = uniqueIdentity("seedcarol");
      const api = new IdentityApi(new RelayClient(relay.baseUrl));
      const created = await createIdentity(api, identity, { hosted: true });
      const { signer, decryptor } = signerFor(created.keys);
      return {
        identity,
        client: new PoweurClient({
          relayUrl: relay.baseUrl,
          signer,
          decryptor,
          sessionStore: new MemorySessionStore(),
          resolve: localResolveOptions(relay.baseUrl),
        }),
      };
    })();

    await alice.client.send(carol.identity, "seed to random");
    expect((await carol.client.inbox()).messages.map((m) => m.plaintext)).toContain(
      "seed to random",
    );

    await carol.client.send(alice.identity, "random to seed");
    expect((await alice.client.inbox()).messages.map((m) => m.plaintext)).toContain(
      "random to seed",
    );
  });

  // The property the whole epic rests on: 32 bytes is enough to come back.
  it("recovers a working identity from the seed alone", async () => {
    await alice.client.send(bob.identity, "recover me");

    // Everything but the seed is discarded; a fresh client is built from it.
    const recovered = clientFromSeed(bob.identity, bob.seed);
    expect(recovered.signer.publicKey).toBe(bob.client.signer.publicKey);

    const { messages } = await recovered.inbox();
    expect(messages.map((m) => m.plaintext)).toContain("recover me");
  });

  it("does not recover with a different seed", async () => {
    const wrong = clientFromSeed(bob.identity, newSeed());
    expect(wrong.signer.publicKey).not.toBe(bob.client.signer.publicKey);
    // Signing with a key the relay never registered must not read the inbox.
    await expect(wrong.inbox()).rejects.toThrow();
  });

  it("keeps the relay's stored encryption key equal to the derived one", async () => {
    const { encryptionPrivateKey } = identityKeysFromSeed(alice.identity, alice.seed);
    const result = await resolveIdentity(alice.identity, localResolveOptions(relay.baseUrl));
    expect(encryptionPrivateKey).toBeDefined();
    expect(result.document.encryption_public_key).toBe(
      alice.client.decryptor?.encryptionPublicKey,
    );
    expect(toBase64url(encryptionPrivateKey!).length).toBeGreaterThan(0);
  });
});
