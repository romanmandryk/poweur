/**
 * Device pairing v2 against a real relay (EPIC-011 E11-T8).
 *
 * Simulates both devices — a new one with no keys, and one that holds the
 * identity — and, for the attacks, a relay that tampers with what it passes
 * on. The properties: the seed crosses sealed end to end; a scanned pairing
 * refuses a swapped key outright; a typed pairing's digits differ when the
 * relay swaps the key, so the person comparing them catches it.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { generateEncryptionKeypair } from "../src/crypto/index.js";
import { identityKeysFromSeed, signerFor, type Signer } from "../src/crypto/keys.js";
import { newSeed } from "../src/crypto/seed.js";
import { randomBytes, toBase64url } from "../src/encoding.js";
import {
  EnrollApi,
  formatShortCode,
  PAIRING_SAS_DIGITS,
  pairingCommitment,
  pairingLink,
  type EnrollSession,
} from "../src/enroll.js";
import { RelayClient, type RequestOptions } from "../src/http.js";
import { createIdentity, IdentityApi } from "../src/identity.js";
import { uniqueIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

/** A relay client that rewrites what the approver is told. */
class TamperingRelay extends RelayClient {
  swap: ((path: string, body: any) => any) | null = null;
  override async request<T>(options: RequestOptions): Promise<T> {
    const out = await super.request<T>(options);
    return (this.swap ? this.swap(options.path, out) : out) as T;
  }
}

describe("device pairing v2 ↔ real relay", () => {
  let relay: RunningRelay;
  let api: EnrollApi;

  interface Party {
    identity: string;
    signer: Signer;
    seed: Uint8Array;
  }

  // One identity per test: the relay caps open offers per identity.
  async function newParty(): Promise<Party> {
    const identity = uniqueIdentity("enroll");
    const seed = newSeed();
    const keys = identityKeysFromSeed(identity, seed);
    await createIdentity(new IdentityApi(new RelayClient(relay.baseUrl)), identity, { hosted: true, keys });
    return { identity, signer: signerFor(keys).signer, seed };
  }

  async function revealed(identity: string, session: EnrollSession, signer: Signer, approver: any, via = api) {
    const newSide = await api.step(identity, session); // the new device reveals
    const approverSide = await via.wait(signer, identity, approver);
    return { newSide, approverSide };
  }

  beforeAll(async () => {
    relay = await startRelay();
    api = new EnrollApi(new RelayClient(relay.baseUrl));
  }, 180_000);

  afterAll(() => relay?.stop());

  it("moves the seed by typed code, with matching digits on both screens", async () => {
    const { identity, signer, seed } = await newParty();
    const session = await api.offer(identity, "Firefox on Linux");
    expect(session.rendezvousId).toMatch(/^[0-9A-HJKMNP-TV-Z]{8}$/);
    // Nothing happens until the approver answers — not even the reveal.
    expect(await api.step(identity, session)).toEqual({ state: "offered" });
    expect(session.revealed).toBe(false);

    const approver = await api.begin(signer, identity, formatShortCode(session.rendezvousId));
    expect(approver.mode).toBe("compare");
    expect(approver.label).toBe("Firefox on Linux");
    expect(await api.wait(signer, identity, approver)).toEqual({ state: "waiting" });

    const { newSide, approverSide } = await revealed(identity, session, signer, approver);
    if (newSide.state !== "compare" || approverSide.state !== "revealed") throw new Error("not revealed");
    expect(newSide.sas).toHaveLength(PAIRING_SAS_DIGITS);
    expect(approverSide.sas).toBe(newSide.sas);

    await api.approve(signer, identity, approver, approverSide, seed);
    const done = await api.step(identity, session);
    if (done.state !== "delivered") throw new Error(`state ${done.state}`);
    expect(toBase64url(done.seed)).toBe(toBase64url(seed));
    // The new device adopts it only because it derives the published key.
    expect(await api.publishedKeyMatches(identity, done.seed)).toBe(true);
    expect(await api.publishedKeyMatches(identity, newSeed())).toBe(false);
    // Single use.
    await expect(api.step(identity, session)).rejects.toThrow();
  });

  it("moves the seed by scanned link, with no digits", async () => {
    const { identity, signer, seed } = await newParty();
    const session = await api.offer(identity);
    const link = pairingLink("https://example.test/app/", identity, session.rendezvousId, session.commitment);
    const approver = await api.begin(signer, identity, link);
    expect(approver.mode).toBe("scan");
    const { newSide, approverSide } = await revealed(identity, session, signer, approver);
    expect(newSide).toEqual({ state: "scan" });
    if (approverSide.state !== "revealed") throw new Error("not revealed");
    await api.approve(signer, identity, approver, approverSide, seed);
    const done = await api.step(identity, session);
    expect(done.state === "delivered" && toBase64url(done.seed)).toBe(toBase64url(seed));
  });

  it("refuses a pairing link for another identity", async () => {
    const { identity, signer } = await newParty();
    const session = await api.offer(identity);
    const link = pairingLink("https://example.test/app/", "someone.poweur.net", session.rendezvousId, session.commitment);
    await expect(api.begin(signer, identity, link)).rejects.toMatchObject({ code: "pairing_mismatch" });
  });

  it("reads a code typed the way a phone keyboard mangles it", async () => {
    const { identity, signer } = await newParty();
    const session = await api.offer(identity);
    const code = session.rendezvousId;
    const messy = `  ${code.slice(0, 4).toLowerCase()}–${code.slice(4)} \n`;
    expect((await api.begin(signer, identity, messy)).code).toBe(code);
  });

  it("refuses an approver that is not the identity", async () => {
    const { identity } = await newParty();
    const session = await api.offer(identity);
    const impostor = signerFor(identityKeysFromSeed(identity, newSeed())).signer;
    await expect(api.begin(impostor, identity, session.rendezvousId)).rejects.toThrow();
  });

  // The attack v2 exists for: the relay hands the approver its own key.
  function attackerCommitment() {
    const key = toBase64url(generateEncryptionKeypair().publicKey);
    const nonce = toBase64url(randomBytes(32));
    return { key, nonce, commitment: pairingCommitment(key, nonce) };
  }

  it("a relay that swaps the key cannot fool a scanned pairing", async () => {
    const { identity, signer } = await newParty();
    const session = await api.offer(identity);
    const evil = new TamperingRelay(relay.baseUrl);
    const attacker = attackerCommitment();
    evil.swap = (path, body) => (path.endsWith("/fetch") ? { ...body, commitment: attacker.commitment } : body);
    const link = pairingLink("https://example.test/app/", identity, session.rendezvousId, session.commitment);
    await expect(new EnrollApi(evil).begin(signer, identity, link)).rejects.toMatchObject({ code: "pairing_mismatch" });
  });

  it("a relay that swaps the key changes the typed pairing's digits", async () => {
    const { identity, signer } = await newParty();
    const session = await api.offer(identity);
    const evil = new TamperingRelay(relay.baseUrl);
    const attacker = attackerCommitment();
    // From the start the approver sees the attacker's commitment, and later
    // the attacker's reveal: a consistent substitute, committed before the
    // approver's nonce existed.
    evil.swap = (path, body) =>
      path.endsWith("/fetch")
        ? {
            ...body,
            commitment: attacker.commitment,
            ...(body.ephemeral_public_key ? { ephemeral_public_key: attacker.key, commit_nonce: attacker.nonce } : {}),
          }
        : body;
    const evilApi = new EnrollApi(evil);
    const approver = await evilApi.begin(signer, identity, session.rendezvousId);
    const { newSide, approverSide } = await revealed(identity, session, signer, approver, evilApi);
    if (newSide.state !== "compare" || approverSide.state !== "revealed") throw new Error("not revealed");
    // The person sees two different numbers and does not approve.
    expect(approverSide.sas).not.toBe(newSide.sas);
    expect(approverSide.ephemeralPublicKey).toBe(attacker.key);
  });

  it("a reveal that does not open the commitment is refused", async () => {
    const { identity, signer } = await newParty();
    const session = await api.offer(identity);
    const evil = new TamperingRelay(relay.baseUrl);
    evil.swap = (path, body) =>
      path.endsWith("/fetch") && body.ephemeral_public_key ? { ...body, ephemeral_public_key: attackerCommitment().key } : body;
    const evilApi = new EnrollApi(evil);
    const approver = await evilApi.begin(signer, identity, session.rendezvousId);
    await api.step(identity, session);
    await expect(evilApi.wait(signer, identity, approver)).rejects.toMatchObject({ code: "pairing_mismatch" });
  });

  it("frees a slot when an offer is cancelled", async () => {
    const { identity } = await newParty();
    const sessions = [];
    for (let i = 0; i < 5; i++) sessions.push(await api.offer(identity));
    await expect(api.offer(identity)).rejects.toThrow();
    await api.cancel(identity, sessions[0]!);
    await expect(api.offer(identity)).resolves.toBeTruthy();
  });
});
