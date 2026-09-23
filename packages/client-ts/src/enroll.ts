/**
 * New-device pairing, v2 (EPIC-011 E11-T8).
 *
 * Moving a seed to a new device needs an **authentic** channel, not a secret
 * one — and the relay is trusted for neither. It must never read the seed,
 * and a compromised relay must not be able to pair a device of its own.
 *
 * v1 showed `SHA-256(ephemeral key) mod 10^6` on both screens. The relay sees
 * that key, so it could grind one of its own with the same six digits in
 * seconds and receive the seed. v2 is commit-then-reveal (Go canonical:
 * packages/identity/pairing.go):
 *
 * 1. The new device commits: it sends `C = SHA-256(K ‖ r)` only.
 * 2. The approver (identity-signed) fetches `C` and contributes a nonce `n`.
 * 3. The new device, seeing `n`, reveals `K` and `r`.
 * 4. The approver checks the reveal opens `C`. **Scanned**, `C` came from the
 *    QR, so nothing needs comparing. **Typed**, both screens show
 *    `pairingSas(C, K, r, n)` — the relay had to fix any substitute before
 *    `n` existed, so it matches with probability 10⁻⁶, once, in the open.
 * 5. The seed is sealed to `K` and delivered, identity-signed.
 *
 * The relay is a blind letterbox: a commitment, nonces, a public key and a
 * sealed blob, none of which it can open.
 */

import { canonicalEnrollAction } from "./canonical.js";
import {
  generateEncryptionKeypair,
  open as openSealed,
  seal,
  sha256Bytes,
  x25519PublicKey,
  type SealedPayload,
} from "./crypto/index.js";
import { identityKeysFromSeed, signerFor, type Signer } from "./crypto/keys.js";
import { fromBase64, randomBytes, rfc3339, toBase64url, utf8 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import type { RelayClient } from "./http.js";
import { newNonce } from "./ids.js";

/** Digits the two screens compare on the typed path. */
export const PAIRING_SAS_DIGITS = 6;
/** Characters in a short code (Crockford base32, 40 bits). */
export const SHORT_CODE_LENGTH = 8;

const SHORT_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/**
 * What a person typed → the canonical code, or null. Spaces and dashes
 * (including the unicode ones phone keyboards substitute) go; case folds;
 * O reads as 0 and I or L as 1. Mirrors `identity.NormalizeShortCode`.
 */
export function normalizeShortCode(input: string): string | null {
  let out = "";
  for (const raw of String(input ?? "")) {
    if (/[\s   ​-‍﻿\-_.‐-―−﹘﹣－]/.test(raw)) continue;
    let c = raw.toUpperCase();
    if (c === "O") c = "0";
    else if (c === "I" || c === "L") c = "1";
    if (c.length !== 1 || !SHORT_ALPHABET.includes(c)) return null;
    out += c;
  }
  return out.length === SHORT_CODE_LENGTH ? out : null;
}

/** "K7QM4XP2" → "K7QM-4XP2", for reading. */
export function formatShortCode(code: string): string {
  return code.length === SHORT_CODE_LENGTH ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
}

/** C for an ephemeral public key and commit nonce (both base64url). */
export function pairingCommitment(ephemeralPublicKey: string, commitNonce: string): string {
  return toBase64url(sha256Bytes(utf8(`poweur/v2/enroll-commit\n${ephemeralPublicKey}\n${commitNonce}`)));
}

/** The six digits both devices show on the typed path. */
export function pairingSas(commitment: string, ephemeralPublicKey: string, commitNonce: string, approverNonce: string): string {
  const d = sha256Bytes(utf8(`poweur/v2/enroll-sas\n${commitment}\n${ephemeralPublicKey}\n${commitNonce}\n${approverNonce}`));
  let n = 0n;
  for (let i = 0; i < 8; i++) n = (n << 8n) | BigInt(d[i]!);
  return String(n % 1_000_000n).padStart(PAIRING_SAS_DIGITS, "0");
}

function b64Len(value: string, bytes: number): boolean {
  try {
    return /^[A-Za-z0-9_-]+$/.test(value) && fromBase64(value).length === bytes;
  } catch {
    return false;
  }
}

/** Throws unless the revealed key and nonce open the commitment. */
export function verifyPairingReveal(commitment: string, ephemeralPublicKey: string, commitNonce: string): void {
  if (!b64Len(ephemeralPublicKey, 32) || !b64Len(commitNonce, 32) || pairingCommitment(ephemeralPublicKey, commitNonce) !== commitment) {
    throw new PoweurError("pairing_mismatch", "The new device's key does not match what it committed to. Do not approve.");
  }
}

/**
 * The QR's content: the approver's app, with the code, commitment and the
 * identity being joined in the fragment (never sent to a server). Mirrors
 * `identity.PairingLink`.
 */
export function pairingLink(appUrl: string, identity: string, code: string, commitment: string): string {
  const id = identity ? `&id=${encodeURIComponent(identity.toLowerCase())}` : "";
  return `${appUrl.replace(/#.*$/, "")}#pair=${code}.${commitment}${id}`;
}

/**
 * The same pairing for the Poweur app: `poweur://` opens the app from a
 * phone's camera, where an https link opens a website holding no keys. The
 * values ride in the query. Mirrors `identity.PairingAppLink`.
 */
export function pairingAppLink(identity: string, code: string, commitment: string): string {
  const id = identity ? `&id=${encodeURIComponent(identity.toLowerCase())}` : "";
  return `poweur://pair?pair=${code}.${commitment}${id}`;
}

export interface PairingLinkParts {
  code: string;
  commitment: string;
  /** Empty when the link does not name one. */
  identity: string;
}

/** A scanned or pasted pairing link (web link, app link, or "CODE.C"), or null. */
export function parsePairingLink(input: string): PairingLinkParts | null {
  let v = String(input ?? "").trim();
  const hash = v.indexOf("#");
  const query = v.indexOf("?");
  if (hash >= 0) v = v.slice(hash + 1);
  else if (query >= 0) v = v.slice(query + 1);
  let identity = "";
  const params = new URLSearchParams(v);
  const pair = params.get("pair");
  if (pair) {
    v = pair;
    identity = (params.get("id") ?? "").trim().toLowerCase();
  }
  v = v.replace(/^pair=/, "");
  const dot = v.lastIndexOf(".");
  if (dot < 0) return null;
  const code = normalizeShortCode(v.slice(0, dot));
  const commitment = v.slice(dot + 1);
  if (!code || !b64Len(commitment, 32)) return null;
  return { code, commitment, identity };
}

/** The new device's half. The private key, nonce and claim token stay here. */
export interface EnrollSession {
  /** The short code ("K7QM4XP2"); show it formatted. */
  rendezvousId: string;
  claimToken: string;
  commitment: string;
  expiresAt: string;
  ephemeralPrivateKey: Uint8Array;
  ephemeralPublicKey: string;
  commitNonce: string;
  revealed: boolean;
}

/** Where the new device's side is. */
export type EnrollStep =
  | { state: "offered" }
  | { state: "scan" }
  | { state: "compare"; sas: string }
  | { state: "delivered"; seed: Uint8Array };

/** The approving device's half of one pairing. */
export interface ApproverSession {
  code: string;
  mode: "scan" | "compare";
  approverNonce: string;
  /** From the scanned link, or the relay's first answer. */
  commitment: string;
  label?: string;
  expiresAt?: string;
}

/** What the approver waits for: the reveal, checked, with its digits. */
export type ApproverStep =
  | { state: "waiting" }
  | { state: "revealed"; ephemeralPublicKey: string; sas: string };

interface FetchResponse {
  rendezvous_id: string;
  state: string;
  commitment: string;
  label?: string;
  expires_at: string;
  ephemeral_public_key?: string;
  commit_nonce?: string;
}

export class EnrollApi {
  readonly #relay: RelayClient;

  constructor(relay: RelayClient) {
    this.#relay = relay;
  }

  #base(identity: string, code?: string): string {
    const root = `/identities/${encodeURIComponent(identity)}/enroll`;
    return code ? `${root}/${encodeURIComponent(code)}` : root;
  }

  /**
   * **New device.** Commit to a fresh ephemeral key and open a pairing.
   * Unauthenticated by necessity — this device has no key yet — so the relay
   * caps concurrent offers per identity.
   */
  async offer(identity: string, label?: string): Promise<EnrollSession> {
    const keypair = generateEncryptionKeypair();
    const ephemeralPublicKey = toBase64url(keypair.publicKey);
    const commitNonce = toBase64url(randomBytes(32));
    const commitment = pairingCommitment(ephemeralPublicKey, commitNonce);
    const offer = await this.#relay.request<{ rendezvous_id: string; claim_token: string; expires_at: string }>({
      method: "POST",
      path: `${this.#base(identity)}/offer`,
      body: { commitment, ...(label ? { label } : {}) },
      allowStatus: [201],
    });
    return {
      rendezvousId: offer.rendezvous_id,
      claimToken: offer.claim_token,
      commitment,
      expiresAt: offer.expires_at,
      ephemeralPrivateKey: keypair.privateKey,
      ephemeralPublicKey,
      commitNonce,
      revealed: false,
    };
  }

  /**
   * **New device.** Advance once: reveal the key when the approver has
   * answered (never before), then collect and open the seed. Call it in a
   * poll; `delivered` consumes the pairing.
   */
  async step(identity: string, session: EnrollSession): Promise<EnrollStep> {
    const poll = await this.#relay.request<{ state: string; approver_nonce?: string; mode?: string; sealed?: string }>({
      method: "GET",
      path: this.#base(identity, session.rendezvousId),
      headers: { Authorization: `Bearer ${session.claimToken}` },
    });
    if (poll.state === "offered") return { state: "offered" };
    if (poll.state === "delivered") {
      let payload: SealedPayload;
      try {
        payload = JSON.parse(poll.sealed ?? "") as SealedPayload;
      } catch {
        throw new PoweurError("decrypt_failed", "sealed payload is not valid JSON");
      }
      const seed = openSealed(session.ephemeralPrivateKey, payload);
      if (seed.length !== 32) throw new PoweurError("decrypt_failed", `expected a 32-byte seed, got ${seed.length}`);
      return { state: "delivered", seed };
    }
    if (!session.revealed) {
      await this.#relay.request({
        method: "POST",
        path: `${this.#base(identity, session.rendezvousId)}/reveal`,
        headers: { Authorization: `Bearer ${session.claimToken}` },
        body: { ephemeral_public_key: session.ephemeralPublicKey, commit_nonce: session.commitNonce },
        allowStatus: [204],
      });
      session.revealed = true;
    }
    if (poll.mode === "scan") return { state: "scan" };
    return {
      state: "compare",
      sas: pairingSas(session.commitment, session.ephemeralPublicKey, session.commitNonce, poll.approver_nonce ?? ""),
    };
  }

  /**
   * **New device.** Does a received seed derive the identity's published
   * signing key? Adopt it only if so: a seed for some other identity must
   * never become this one's keys.
   */
  async publishedKeyMatches(identity: string, seed: Uint8Array): Promise<boolean> {
    const published = await this.#relay.request<{ public_key?: string }>({
      method: "GET",
      path: `/identities/${encodeURIComponent(identity)}`,
    });
    const strip = (key: string) => key.replace(/^ed25519:/, "");
    return strip(published.public_key ?? "") === strip(signerFor(identityKeysFromSeed(identity, seed)).signer.publicKey);
  }

  /** **New device.** Abandon the pairing, freeing its slot. */
  async cancel(identity: string, session: Pick<EnrollSession, "rendezvousId" | "claimToken">): Promise<void> {
    await this.#relay.request({
      method: "DELETE",
      path: this.#base(identity, session.rendezvousId),
      headers: { Authorization: `Bearer ${session.claimToken}` },
      allowStatus: [204],
    });
  }

  /**
   * **Approving device.** Start approving from a scanned/pasted pairing link
   * or a typed code: contribute this side's nonce and learn what is asking.
   */
  async begin(signer: Signer, identity: string, input: string): Promise<ApproverSession> {
    const link = parsePairingLink(input);
    const code = link?.code ?? normalizeShortCode(input);
    if (!code) throw new PoweurError("invalid_code", "That is not a pairing code (8 characters, like K7QM-4XP2).");
    if (link?.identity && link.identity !== identity.toLowerCase()) {
      throw new PoweurError("pairing_mismatch", `This pairing link is for ${link.identity}, not ${identity}.`);
    }
    const session: ApproverSession = {
      code,
      mode: link ? "scan" : "compare",
      approverNonce: toBase64url(randomBytes(32)),
      commitment: link?.commitment ?? "",
    };
    const got = await this.#fetch(signer, identity, session);
    // A scanned commitment is the device the user is looking at. The relay's
    // copy must be the same one, or this code belongs to someone else.
    if (link && got.commitment !== link.commitment) {
      throw new PoweurError("pairing_mismatch", "This code belongs to a different device than the one you scanned. Do not approve.");
    }
    session.commitment = got.commitment;
    session.label = got.label;
    session.expiresAt = got.expires_at;
    return session;
  }

  /**
   * **Approving device.** Has the new device revealed its key? When it has,
   * the reveal is checked against the commitment here — never on the relay's
   * word — and the digits computed. On the typed path, show them and deliver
   * only after the person confirms both screens match.
   */
  async wait(signer: Signer, identity: string, session: ApproverSession): Promise<ApproverStep> {
    const got = await this.#fetch(signer, identity, session);
    if (got.state === "nonce" || !got.ephemeral_public_key || !got.commit_nonce) return { state: "waiting" };
    if (got.commitment !== session.commitment) {
      throw new PoweurError("pairing_mismatch", "The request changed while you were approving it. Do not approve.");
    }
    verifyPairingReveal(session.commitment, got.ephemeral_public_key, got.commit_nonce);
    return {
      state: "revealed",
      ephemeralPublicKey: got.ephemeral_public_key,
      sas: pairingSas(session.commitment, got.ephemeral_public_key, got.commit_nonce, session.approverNonce),
    };
  }

  /** **Approving device.** Seal the seed to the checked key and deliver it. */
  async approve(
    signer: Signer,
    identity: string,
    session: ApproverSession,
    revealed: Extract<ApproverStep, { state: "revealed" }>,
    seed: Uint8Array,
  ): Promise<void> {
    const sealed = seal(revealed.ephemeralPublicKey, seed);
    await this.#relay.request({
      method: "POST",
      path: `${this.#base(identity, session.code)}/deliver`,
      body: { ...(await this.#signed(signer, "enroll-deliver", identity, session.code)), sealed: JSON.stringify(sealed) },
      allowStatus: [204],
    });
  }

  async #fetch(signer: Signer, identity: string, session: ApproverSession): Promise<FetchResponse> {
    return this.#relay.request<FetchResponse>({
      method: "POST",
      path: `${this.#base(identity, session.code)}/fetch`,
      body: {
        ...(await this.#signed(signer, "enroll-fetch", identity, session.code)),
        approver_nonce: session.approverNonce,
        mode: session.mode,
      },
    });
  }

  async #signed(signer: Signer, action: string, identity: string, code: string): Promise<Record<string, string>> {
    const issuedAt = rfc3339();
    const nonce = newNonce();
    const canonical = canonicalEnrollAction(action, identity.toLowerCase(), code, issuedAt, nonce);
    return { issued_at: issuedAt, nonce, identity_signature: await signer.sign(canonical) };
  }
}

/** Public key for an ephemeral private key — for callers rebuilding a session. */
export function ephemeralPublicKeyOf(privateKey: Uint8Array): string {
  return toBase64url(x25519PublicKey(privateKey));
}
