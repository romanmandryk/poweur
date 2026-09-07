/**
 * Keystore endpoints against a real Go relay (EPIC-011 E11-T1).
 *
 * The interesting half is the bootstrap read: it must work with **no identity
 * key at all**, because that is the situation it exists for. These tests build
 * genuine WebAuthn assertions with an Ed25519 authenticator (COSE alg -8, one
 * of the algorithms the web client requests) rather than stubbing them.
 */

import { webcrypto } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { identityKeysFromSeed, signerFor, type Signer } from "../src/crypto/keys.js";
import { newSeed } from "../src/crypto/seed.js";
import { toBase64url, utf8 } from "../src/encoding.js";
import { RelayClient } from "../src/http.js";
import { createIdentity, IdentityApi } from "../src/identity.js";
import { KeystoreApi, type WebAuthnAssertion, type WrappedBlob } from "../src/keystore.js";
import { uniqueIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

const RP_ID = "poweur.net";
const COSE_EDDSA = -8;
const FLAG_UP = 0x01;
const FLAG_UV = 0x04;

/** A stand-in authenticator: an Ed25519 key that can produce assertions. */
class FakeAuthenticator {
  constructor(
    readonly credentialId: string,
    readonly spki: string,
    readonly privateKey: CryptoKey,
    readonly rpId: string = RP_ID,
  ) {}

  static async create(credentialId: string, rpId = RP_ID): Promise<FakeAuthenticator> {
    const pair = (await webcrypto.subtle.generateKey({ name: "Ed25519" }, true, [
      "sign",
      "verify",
    ])) as CryptoKeyPair;
    const spki = new Uint8Array(await webcrypto.subtle.exportKey("spki", pair.publicKey));
    return new FakeAuthenticator(credentialId, toBase64url(spki), pair.privateKey, rpId);
  }

  async assert(challenge: string, flags = FLAG_UP | FLAG_UV): Promise<WebAuthnAssertion> {
    const clientData = utf8(
      JSON.stringify({
        type: "webauthn.get",
        challenge: toBase64url(utf8(challenge)),
        origin: `https://${this.rpId}`,
      }),
    );
    const rpHash = new Uint8Array(
      await webcrypto.subtle.digest("SHA-256", utf8(this.rpId)),
    );
    const authData = new Uint8Array(37);
    authData.set(rpHash, 0);
    authData[32] = flags;
    authData[36] = 1; // sign counter

    const clientHash = new Uint8Array(await webcrypto.subtle.digest("SHA-256", clientData));
    const signed = new Uint8Array(authData.length + clientHash.length);
    signed.set(authData, 0);
    signed.set(clientHash, authData.length);

    const sig = new Uint8Array(
      await webcrypto.subtle.sign({ name: "Ed25519" }, this.privateKey, signed),
    );
    return {
      credential_id: this.credentialId,
      client_data_json: toBase64url(clientData),
      authenticator_data: toBase64url(authData),
      signature: toBase64url(sig),
    };
  }
}

const wrapped: WrappedBlob = { iv: "aXYtYnl0ZXM", ciphertext: "d3JhcHBlZC1zZWVk" };

describe("keystore ↔ real relay", () => {
  let relay: RunningRelay;
  let api: KeystoreApi;
  let identity: string;
  let signer: Signer;
  let auth: FakeAuthenticator;
  let masterAuth: FakeAuthenticator | undefined;

  /** A fresh single-use challenge from the relay. */
  async function challenge(): Promise<string> {
    const res = await fetch(
      `${relay.baseUrl}/auth/challenge?identity=${encodeURIComponent(identity)}`,
    );
    return ((await res.json()) as { challenge: string }).challenge;
  }

  beforeAll(async () => {
    relay = await startRelay();
    identity = uniqueIdentity("keystore");
    const relayClient = new RelayClient(relay.baseUrl);
    const keys = identityKeysFromSeed(identity, newSeed());
    await createIdentity(new IdentityApi(relayClient), identity, { hosted: true, keys });
    signer = signerFor(keys).signer;
    api = new KeystoreApi(relayClient);
    auth = await FakeAuthenticator.create(toBase64url(utf8("cred-primary")));
  }, 180_000);

  afterAll(() => relay?.stop());

  it("enrolls a wrapped seed copy signed by the identity key", async () => {
    const result = await api.enroll(signer, identity, {
      enrollmentId: "enr-1",
      kind: "passkey",
      wrap: "prf",
      wrapped,
      credentialId: auth.credentialId,
      credentialPublicKey: auth.spki,
      credentialAlg: COSE_EDDSA,
      label: "Laptop",
    });
    expect(result.enrollment_id).toBe("enr-1");
    expect(result.created_at).toBeTruthy();
  });

  it("fetches the wrapped copy with an assertion and no identity key", async () => {
    const result = await api.fetch(identity, await auth.assert(await challenge()), RP_ID);
    expect(result.entries).toHaveLength(1);
    const entry = result.entries[0]!;
    expect(entry.enrollment_id).toBe("enr-1");
    // Ciphertext round-trips untouched — the relay never interprets it.
    expect(entry.wrapped).toEqual(wrapped);
    expect(entry.label).toBe("Laptop");
  });

  it("lists enrollments from the identity key alone, with no ciphertext", async () => {
    // The inventory screen runs while you are unlocked, so prompting an
    // authenticator for it would be a prompt for nothing.
    const { identity: listed, enrollments } = await api.list(signer, identity);

    expect(listed).toBe(identity.toLowerCase());
    expect(enrollments).toHaveLength(1);
    const summary = enrollments[0]!;
    expect(summary).toMatchObject({
      enrollment_id: "enr-1",
      kind: "passkey",
      wrap: "prf",
      payload: "seed",
      label: "Laptop",
      has_passkey: true,
    });
    // Neither the wrapped seed nor the credential id is in the listing.
    expect(JSON.stringify(summary)).not.toContain(wrapped.ciphertext);
    expect(JSON.stringify(summary)).not.toContain(auth.credentialId);
  });

  it("defaults the listed identity to the signer's own", async () => {
    const { enrollments } = await api.list(signer);
    expect(enrollments.map((e) => e.enrollment_id)).toContain("enr-1");
  });

  it("rejects a replayed assertion", async () => {
    const assertion = await auth.assert(await challenge());
    await expect(api.fetch(identity, assertion, RP_ID)).resolves.toBeTruthy();
    await expect(api.fetch(identity, assertion, RP_ID)).rejects.toThrow();
  });

  it("rejects an assertion from an authenticator that is not enrolled", async () => {
    const other = await FakeAuthenticator.create(toBase64url(utf8("cred-other")));
    await expect(api.fetch(identity, await other.assert(await challenge()), RP_ID)).rejects.toThrow();
  });

  it("rejects an assertion without user verification", async () => {
    const assertion = await auth.assert(await challenge(), FLAG_UP);
    await expect(api.fetch(identity, assertion, RP_ID)).rejects.toThrow();
  });

  it("refuses a passkey enrollment with no verifiable public key", async () => {
    await expect(
      api.enroll(signer, identity, {
        enrollmentId: "enr-bad",
        kind: "passkey",
        wrap: "prf",
        wrapped,
        credentialId: auth.credentialId,
        // credentialPublicKey deliberately omitted
      }),
    ).rejects.toThrow(/together/);
  });

  it("rejects an enrollment signed by the wrong key", async () => {
    const impostor = signerFor(identityKeysFromSeed(identity, newSeed())).signer;
    await expect(
      api.enroll(impostor, identity, {
        enrollmentId: "enr-evil",
        kind: "passkey",
        wrap: "prf",
        wrapped,
      }),
    ).rejects.toThrow();
  });

  it("supports several enrollments and returns them all", async () => {
    const second = await FakeAuthenticator.create(toBase64url(utf8("cred-yubikey")));
    masterAuth = second;
    await api.enroll(signer, identity, {
      enrollmentId: "enr-2",
      kind: "hardware-key",
      wrap: "prf",
      wrapped,
      credentialId: second.credentialId,
      credentialPublicKey: second.spki,
      credentialAlg: COSE_EDDSA,
      role: "recovery-master",
      label: "YubiKey",
    });
    // Either authenticator can bootstrap, and both see the full set.
    const viaSecond = await api.fetch(identity, await second.assert(await challenge()), RP_ID);
    expect(viaSecond.entries.map((e) => e.enrollment_id).sort()).toEqual(["enr-1", "enr-2"]);
    expect(viaSecond.entries.find((e) => e.enrollment_id === "enr-2")?.role).toBe(
      "recovery-master",
    );
  });

  // enr-2 was enrolled as recovery-master above, so this also exercises the
  // gating: the removal only succeeds when it carries that authenticator's
  // assertion.
  it("removes an enrollment, denying it the bootstrap read", async () => {
    const master = masterAuth!;
    await expect(api.remove(signer, identity, "enr-1")).rejects.toThrow(/recovery-master/);

    await api.remove(signer, identity, "enr-1", {
      actorAssertion: await master.assert(await challenge()),
      rpId: RP_ID,
    });
    await expect(api.fetch(identity, await auth.assert(await challenge()), RP_ID)).rejects.toThrow();
  });

  it("refuses to remove the last enrollment without an explicit override", async () => {
    const master = masterAuth!;
    await expect(
      api.remove(signer, identity, "enr-2", {
        actorAssertion: await master.assert(await challenge()),
        rpId: RP_ID,
      }),
    ).rejects.toThrow(/only enrollment|recovery would be impossible/);

    await api.remove(signer, identity, "enr-2", {
      actorAssertion: await master.assert(await challenge()),
      rpId: RP_ID,
      allowLast: true,
    });
  });
});
