/**
 * The web client's keystore path against a real Go relay (EPIC-011).
 *
 * The one thing worth proving end to end is the **bootstrap**: after this
 * browser forgets everything, an authenticator assertion alone must be able to
 * fetch the wrapped seed back and open it. Everything else in EPIC-011 is
 * convenience; that is the property that makes losing a device survivable, and
 * it cannot be asserted against a stub relay.
 *
 * WebAuthn itself is not available in Node, so the authenticator is simulated
 * — a real Ed25519 key producing real assertions the relay verifies, with the
 * PRF output stood in for by a fixed secret. The parts under test are the
 * relay contract and this app's wrapping, not the browser's WebAuthn stack;
 * `test/e2e/` drives the real thing.
 */
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { webcrypto } from "node:crypto";
import {
  KeystoreApi, RelayClient, createIdentity, IdentityApi,
  crypto as sdk, toBase64url, utf8,
} from "@poweur/client";

import "./helpers/browser-globals.mjs";
import { startRelay } from "./helpers/relay.mjs";
import { clientFor, identityApiFor } from "../js/client.js";
import { listEnrollments, enrollThisBrowser, removeEnrollment } from "../js/keystore.js";
import {
  saveIdentityRecord, loadIdentityRecord, setActiveIdentity, setUnlockedKeys, clearUnlockedKeys,
} from "../js/storage.js";
import { jwksFromSeed, unwrapKeysAES, wrapKeysAES } from "../js/vault.js";

const RP_ID = "poweur.net";
const COSE_EDDSA = -8;

/** A stand-in authenticator: a real Ed25519 key that produces real assertions. */
class FakeAuthenticator {
  constructor(credentialId, spki, privateKey) {
    this.credentialId = credentialId;
    this.spki = spki;
    this.privateKey = privateKey;
    /** Stands in for the PRF output this authenticator would derive. */
    this.prf = webcrypto.getRandomValues(new Uint8Array(32));
  }

  static async create(label) {
    const pair = await webcrypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
    const spki = new Uint8Array(await webcrypto.subtle.exportKey("spki", pair.publicKey));
    return new FakeAuthenticator(toBase64url(utf8(label)), toBase64url(spki), pair.privateKey);
  }

  async assert(challenge) {
    const clientData = utf8(JSON.stringify({
      type: "webauthn.get",
      challenge: toBase64url(utf8(challenge)),
      origin: `https://${RP_ID}`,
    }));
    const rpHash = new Uint8Array(await webcrypto.subtle.digest("SHA-256", utf8(RP_ID)));
    const authData = new Uint8Array(37);
    authData.set(rpHash, 0);
    authData[32] = 0x01 | 0x04; // user present + user verified
    authData[36] = 1;
    const clientHash = new Uint8Array(await webcrypto.subtle.digest("SHA-256", clientData));
    const signed = new Uint8Array(authData.length + clientHash.length);
    signed.set(authData, 0);
    signed.set(clientHash, authData.length);
    const signature = new Uint8Array(
      await webcrypto.subtle.sign({ name: "Ed25519" }, this.privateKey, signed),
    );
    return {
      credential_id: this.credentialId,
      client_data_json: toBase64url(clientData),
      authenticator_data: toBase64url(authData),
      signature: toBase64url(signature),
    };
  }
}

describe("web keystore ↔ real relay", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;
  let identity;
  let seed;
  let laptop;

  beforeAll(async () => {
    relay = await startRelay();
    identity = `keys${Date.now().toString(36)}.poweur.net`;
    seed = sdk.newSeed();
    laptop = await FakeAuthenticator.create("laptop-credential");

    const { signingJWK, encJWK, publicKey, encPublicKey } = jwksFromSeed(seed);
    await createIdentity(identityApiFor(relay.baseUrl), identity, {
      hosted: true,
      keys: {
        identity,
        signingPrivateKey: sdk.deriveSigningKey(seed).privateKey,
        encryptionPrivateKey: sdk.deriveEncryptionKey(seed).privateKey,
      },
    });

    // What registration writes locally: keys wrapped under the authenticator's
    // PRF secret, with the seed inside so a kit stays possible.
    saveIdentityRecord(identity, {
      identity, publicKey, encPublicKey,
      credentialId: laptop.credentialId,
      credentialPublicKey: laptop.spki,
      credentialAlg: COSE_EDDSA,
      encryptedKeys: {
        ...(await wrapKeysAES(laptop.prf, signingJWK, encJWK, toBase64url(seed))),
        kdf: "prf",
      },
      relay: relay.baseUrl,
      createdAt: new Date().toISOString(),
      supportsPRF: true,
      seedDerived: true,
    });
    setActiveIdentity(identity);
    setUnlockedKeys(identity, signingJWK, encJWK, toBase64url(seed));
  }, 180_000);

  afterAll(() => relay?.stop());

  it("registers this browser in the relay keystore", async () => {
    const { enrollmentId, canBootstrap } = await enrollThisBrowser(clientFor(identity), identity, {
      label: "Chrome on Mac",
    });
    expect(enrollmentId).toBeTruthy();
    expect(canBootstrap).toBe(true);
    // Persisted, so the inventory can tell which row is this device.
    expect(loadIdentityRecord(identity).enrollmentId).toBe(enrollmentId);
  });

  it("lists it back, marked as this device, with no ciphertext", async () => {
    const enrollments = await listEnrollments(clientFor(identity), identity);
    expect(enrollments).toHaveLength(1);
    expect(enrollments[0]).toMatchObject({
      label: "Chrome on Mac",
      kind: "passkey",
      wrap: "prf",
      payload: "seed",
      current: true,
    });
    const record = loadIdentityRecord(identity);
    expect(JSON.stringify(enrollments)).not.toContain(record.encryptedKeys.ciphertext);
  });

  it("is idempotent — re-enrolling replaces the same row rather than adding one", async () => {
    await enrollThisBrowser(clientFor(identity), identity, { label: "Chrome on Mac (again)" });
    const enrollments = await listEnrollments(clientFor(identity), identity);
    expect(enrollments).toHaveLength(1);
    expect(enrollments[0].label).toBe("Chrome on Mac (again)");
  });

  it("recovers the identity from an assertion alone, with no identity key", async () => {
    // The scenario: this browser's site data is gone. No key, no record — the
    // authenticator is all that is left.
    const record = loadIdentityRecord(identity);
    clearUnlockedKeys();

    const api = new KeystoreApi(new RelayClient(relay.baseUrl));
    const assertion = await laptop.assert(await api.challenge(identity));
    const { entries } = await api.fetch(identity, assertion, RP_ID);

    expect(entries).toHaveLength(1);
    const opened = await unwrapKeysAES(laptop.prf, entries[0].wrapped);

    // Everything is back: both keys and the seed behind the recovery kit.
    expect(opened.signingJWK).toEqual(
      (await unwrapKeysAES(laptop.prf, record.encryptedKeys)).signingJWK,
    );
    expect(opened.seed).toBe(toBase64url(seed));

    setUnlockedKeys(identity, opened.signingJWK, opened.encJWK, opened.seed);
  });

  it("refuses a stranger's authenticator", async () => {
    const attacker = await FakeAuthenticator.create("attacker-credential");
    const api = new KeystoreApi(new RelayClient(relay.baseUrl));
    const assertion = await attacker.assert(await api.challenge(identity));
    await expect(api.fetch(identity, assertion, RP_ID)).rejects.toThrow();
  });

  it("removes another device and leaves this one alone", async () => {
    const client = clientFor(identity);
    const phone = await FakeAuthenticator.create("phone-credential");
    await client.keystore.enroll(client.signer, identity, {
      enrollmentId: "phone-enrollment",
      kind: "passkey",
      wrap: "prf",
      wrapped: { iv: "aXY", ciphertext: "Y3Q" },
      credentialId: phone.credentialId,
      credentialPublicKey: phone.spki,
      credentialAlg: COSE_EDDSA,
      label: "Safari on iOS",
    });
    expect(await listEnrollments(client, identity)).toHaveLength(2);

    await removeEnrollment(client, identity, "phone-enrollment");

    const left = await listEnrollments(client, identity);
    expect(left).toHaveLength(1);
    expect(left[0].current).toBe(true);
  });

  it("refuses to enroll a browser whose passkey exposed no public key", async () => {
    const record = loadIdentityRecord(identity);
    saveIdentityRecord(identity, { ...record, credentialPublicKey: undefined });
    // Better to say so than to store a copy no assertion can ever unlock.
    await expect(enrollThisBrowser(clientFor(identity), identity)).rejects.toThrow(/public key/i);
    saveIdentityRecord(identity, record);
  });
});
