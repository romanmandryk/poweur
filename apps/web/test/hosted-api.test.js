import { describe, it, expect, beforeAll, afterAll } from "vitest";
import {
  generateSigningKeypair,
  generateEncryptionKeypair,
  sign,
  buildSignedIdentityDocument,
  canonicalIdentityRegistration,
  now,
  randomNonce,
  toBase64url,
} from "../js/crypto.js";
import {
  registerIdentity,
  fetchIdentityDocument,
  lookupIdentity,
  checkHealth,
  fetchRelayAddress,
} from "../js/api.js";
import { startRelay } from "./helpers/relay.mjs";

describe("web client ↔ real relay (hosted identity)", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  beforeAll(async () => {
    relay = await startRelay();
  }, 60_000);

  afterAll(() => {
    relay?.stop();
  });

  it("health and relay address", async () => {
    const h = await checkHealth(relay.baseUrl);
    expect(h.status).toBe("ok");
    const addr = await fetchRelayAddress(relay.baseUrl);
    expect(addr).toBe(relay.addr);
  });

  it("registers hosted identity with signed identity_document", async () => {
    const { publicKeyBytes: sigPub, privateKeyJWK: sigPriv } = await generateSigningKeypair();
    const { publicKeyBytes: encPub } = await generateEncryptionKeypair();
    const pubB64 = toBase64url(sigPub);
    const encB64 = toBase64url(encPub);
    const identity = `webvitest${Date.now()}.poweur.net`;
    const issuedAt = now();
    const nonce = randomNonce();
    const relayAddr = await fetchRelayAddress(relay.baseUrl);

    const identitySignature = await sign(
      sigPriv,
      canonicalIdentityRegistration(identity, pubB64, encB64, relayAddr, issuedAt, nonce),
    );
    const identityDocument = await buildSignedIdentityDocument(sigPriv, {
      identity,
      publicKey: pubB64,
      encPublicKey: encB64,
      relay: relayAddr,
      updatedAt: issuedAt,
    });

    const resp = await registerIdentity(relay.baseUrl, {
      identity,
      public_key: pubB64,
      encryption_public_key: encB64,
      issued_at: issuedAt,
      nonce,
      identity_signature: identitySignature,
      identity_document: identityDocument,
    });

    expect(resp.identity).toBe(identity);
    expect(resp.public_key).toBeTruthy();

    const doc = await fetchIdentityDocument(identity, { relayUrl: relay.baseUrl });
    expect(doc.identity).toBe(identity);
    expect(doc.public_key).toContain(pubB64);

    const lookup = await lookupIdentity(identity, { relayUrl: relay.baseUrl });
    expect(lookup.source).toBe("web");
    expect(lookup.document.encryption_public_key).toBeTruthy();
  });

  it("rejects reserved hosted handle", async () => {
    const { publicKeyBytes: sigPub, privateKeyJWK: sigPriv } = await generateSigningKeypair();
    const pubB64 = toBase64url(sigPub);
    const identity = "www.poweur.net";
    const issuedAt = now();
    const nonce = randomNonce();
    const relayAddr = await fetchRelayAddress(relay.baseUrl);
    const identitySignature = await sign(
      sigPriv,
      canonicalIdentityRegistration(identity, pubB64, "", relayAddr, issuedAt, nonce),
    );
    const identityDocument = await buildSignedIdentityDocument(sigPriv, {
      identity,
      publicKey: pubB64,
      encPublicKey: "",
      relay: relayAddr,
      updatedAt: issuedAt,
    });

    await expect(
      registerIdentity(relay.baseUrl, {
        identity,
        public_key: pubB64,
        issued_at: issuedAt,
        nonce,
        identity_signature: identitySignature,
        identity_document: identityDocument,
      }),
    ).rejects.toThrow();
  });
});
