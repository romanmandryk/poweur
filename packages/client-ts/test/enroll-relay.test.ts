/**
 * New-device enrollment ceremony against a real relay (EPIC-011 E11-T3).
 *
 * Simulates both devices: a new one with no keys, and one that already holds
 * the identity. The property under test is that 32 bytes cross between them
 * with the relay seeing only ciphertext, authenticated by a six-digit code
 * that a human compares.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { identityKeysFromSeed, signerFor, type Signer } from "../src/crypto/keys.js";
import { newSeed } from "../src/crypto/seed.js";
import { toBase64url } from "../src/encoding.js";
import { computeSas, EnrollApi, SAS_DIGITS } from "../src/enroll.js";
import { RelayClient } from "../src/http.js";
import { createIdentity, IdentityApi } from "../src/identity.js";
import { uniqueIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

describe("device enrollment ceremony ↔ real relay", () => {
  let relay: RunningRelay;
  let api: EnrollApi;

  interface Party {
    identity: string;
    signer: Signer;
    seed: Uint8Array;
  }

  /**
   * A fresh identity per test. The relay caps concurrent enrollment offers, so
   * sharing one identity across tests exhausts the cap rather than exercising
   * anything — and each test wants a clean rendezvous space anyway.
   */
  async function newParty(): Promise<Party> {
    const identity = uniqueIdentity("enroll");
    const seed = newSeed();
    const keys = identityKeysFromSeed(identity, seed);
    await createIdentity(new IdentityApi(new RelayClient(relay.baseUrl)), identity, {
      hosted: true,
      keys,
    });
    return { identity, signer: signerFor(keys).signer, seed };
  }

  beforeAll(async () => {
    relay = await startRelay();
    api = new EnrollApi(new RelayClient(relay.baseUrl));
  }, 180_000);

  afterAll(() => relay?.stop());

  it("moves the seed to a new device, sealed end to end", async () => {
    const { identity, signer, seed } = await newParty();

    // New device: no keys at all, just an ephemeral pair and a code to read out.
    const session = await api.offer(identity, "Firefox on Linux");
    expect(session.sas).toHaveLength(SAS_DIGITS);
    expect(session.sas).toBe(computeSas(session.ephemeralPublicKey));

    // Nothing to collect until the other side approves.
    expect(await api.claim(identity, session)).toBeNull();

    // Approving device: the user types the code, and both screens must agree.
    const pending = await api.pending(signer, identity, session.rendezvousId);
    expect(pending.sas).toBe(session.sas);
    expect(pending.label).toBe("Firefox on Linux");

    await api.approve(signer, identity, pending, seed);

    const received = await api.claim(identity, session);
    expect(received).not.toBeNull();
    expect(toBase64url(received!)).toBe(toBase64url(seed));
  });

  it("finds the offer even when the request code was pasted with wrapping noise", async () => {
    const { identity, signer } = await newParty();
    const session = await api.offer(identity);
    // Spaces, a newline, zero-width chars, an en-dash — the shape a phone
    // keyboard produces when the user copies the code off the other screen.
    const messy = `  ${session.rendezvousId.replace("-", "\u2013")}\n`.replace(
      /(.{4})/,
      "$1\u200b",
    );
    const pending = await api.pending(signer, identity, messy);
    expect(pending.sas).toBe(session.sas);
  });

  it("consumes the rendezvous, so a captured id cannot be replayed", async () => {
    const { identity, signer, seed } = await newParty();
    const session = await api.offer(identity);
    const pending = await api.pending(signer, identity, session.rendezvousId);
    await api.approve(signer, identity, pending, seed);
    expect(await api.claim(identity, session)).not.toBeNull();
    await expect(api.claim(identity, session)).rejects.toThrow();
  });

  it("derives the code from the key, not from what the relay says", async () => {
    const { identity, signer } = await newParty();
    const session = await api.offer(identity);
    // Two independent computations of the same value — this is what the user
    // compares, and neither side takes the relay's word for it.
    const pending = await api.pending(signer, identity, session.rendezvousId);
    expect(computeSas(pending.ephemeral_public_key)).toBe(session.sas);

    // A different ephemeral key must yield a different code, or comparison
    // would authenticate nothing.
    const other = await api.offer(identity);
    expect(other.sas).not.toBe(session.sas);
  });

  it("refuses approval signed by a key that is not the identity's", async () => {
    const { identity, signer, seed } = await newParty();
    const session = await api.offer(identity);
    const pending = await api.pending(signer, identity, session.rendezvousId);
    const impostor = signerFor(identityKeysFromSeed(identity, newSeed())).signer;
    await expect(api.approve(impostor, identity, pending, seed)).rejects.toThrow();
    await expect(api.pending(impostor, identity, session.rendezvousId)).rejects.toThrow();
  });

  // Redirecting an approval is not expressible through this API: `approve`
  // signs for the rendezvous it delivers to. (The relay-side binding — a
  // signature for one rendezvous rejected at another — is asserted in
  // apps/api/internal/relay/enroll_test.go, where it can be forged.) What the
  // SDK can show is that misdirecting the delivery gains nothing: the payload
  // is sealed to the *approved* device's key, so the other rendezvous receives
  // ciphertext it cannot open.
  it("seals to the approved device, so a misdirected delivery is useless", async () => {
    const { identity, signer, seed } = await newParty();
    const victim = await api.offer(identity);
    const attacker = await api.offer(identity);
    const pendingVictim = await api.pending(signer, identity, victim.rendezvousId);

    await api.approve(
      signer,
      identity,
      { ...pendingVictim, rendezvous_id: attacker.rendezvousId },
      seed,
    );

    // The attacker holds the rendezvous but not the key it was sealed to.
    await expect(api.claim(identity, attacker)).rejects.toThrow();
  });

  it("rejects a payload sealed to a different ephemeral key", async () => {
    const { identity, signer, seed } = await newParty();
    const target = await api.offer(identity);
    const eavesdropper = await api.offer(identity);
    const pending = await api.pending(signer, identity, target.rendezvousId);
    await api.approve(signer, identity, pending, seed);

    // The eavesdropper knows the target's rendezvous id but not its private
    // key; claiming with the wrong session must fail to open.
    const wrongSession = { ...eavesdropper, rendezvousId: target.rendezvousId };
    await expect(api.claim(identity, wrongSession)).rejects.toThrow();
  });

  // Abandoned ceremonies must not lock an identity out until they expire.
  it("frees a slot when an offer is cancelled", async () => {
    const { identity } = await newParty();
    const sessions = [];
    for (let i = 0; i < 5; i += 1) sessions.push(await api.offer(identity));
    await expect(api.offer(identity)).rejects.toThrow(/maximum number of open/);

    await api.cancel(identity, sessions[0]!);
    const replacement = await api.offer(identity);
    expect(replacement.rendezvousId).toBeTruthy();
  });
});
