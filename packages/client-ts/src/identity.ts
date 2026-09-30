/**
 * Identity lifecycle: create, register (hosted or DNS-token), publish and
 * rotate encryption keys, export, rotate signing keys, look up.
 *
 * One function per relay endpoint that exists today; the signing always goes
 * through the `Signer` seam so a passkey-held key and an agent's file key are
 * interchangeable here.
 */

import {
  canonicalIdentityExport,
  canonicalIdentityRegistration,
  canonicalIdentityRotation,
} from "./canonical.js";
import { generateSeedIdentity, signerFor, type Signer, type StoredIdentityKeys } from "./crypto/keys.js";
import { x25519PublicKey, ed25519PublicKey } from "./crypto/index.js";
import { newDocument, signDocument } from "./document.js";
import { rfc3339, stripKeyPrefix, toBase64url } from "./encoding.js";
import { PoweurError, RelayError } from "./errors.js";
import { newNonce } from "./ids.js";
import { RelayClient, type RelayClientOptions } from "./http.js";
import { solvePow } from "./pow.js";
import type {
  HandleAvailability,
  HealthResponse,
  IdentityDocument,
  IdentityResponse,
  RelayRoot,
} from "./types.js";

export interface RegisterIdentityRequest {
  identity: string;
  public_key: string;
  encryption_public_key?: string;
  dns_provider?: string;
  dns_token?: string;
  invite_code?: string;
  pow_token?: string;
  pow_solution?: string;
  identity_document?: IdentityDocument;
  issued_at: string;
  nonce: string;
  identity_signature: string;
}

export class IdentityApi {
  readonly client: RelayClient;

  constructor(relayUrl: string | RelayClient, options: RelayClientOptions = {}) {
    this.client =
      relayUrl instanceof RelayClient ? relayUrl : new RelayClient(relayUrl, options);
  }

  health(): Promise<HealthResponse> {
    return this.client.request<HealthResponse>({ method: "GET", path: "/health" });
  }

  /**
   * Can this handle be claimed here (EPIC-018 E18-T2)?
   *
   * Called before the passkey ceremony, not after: the relay's answer carries
   * a `reason` and the policy it applied, so a client can say *why* a name is
   * unavailable and validate the next attempt inline.
   */
  availability(handle: string, domain?: string): Promise<HandleAvailability> {
    const query = new URLSearchParams({ handle });
    if (domain) query.set("domain", domain);
    return this.client.request<HandleAvailability>({
      method: "GET",
      path: `/hosted/availability?${query.toString()}`,
    });
  }

  /**
   * The relay's service banner (EPIC-015 E15-T7).
   *
   * It carries the launcher hosts and hosted domains a client needs to work
   * out which front door it is serving, so this is the one unauthenticated
   * read that happens before anything else — including before an identity
   * exists.
   */
  root(): Promise<RelayRoot> {
    return this.client.request<RelayRoot>({ method: "GET", path: "/" });
  }

  /**
   * The relay's canonical address (host[:port]). Registration signatures bind
   * to it, so a captured signature cannot be replayed at another relay.
   */
  async relayAddress(): Promise<string> {
    const root = await this.root();
    if (!root.relay_address) {
      throw new PoweurError("relay_error", "relay did not return its address");
    }
    return root.relay_address;
  }

  register(request: RegisterIdentityRequest): Promise<IdentityResponse> {
    return this.client.request<IdentityResponse>({
      method: "POST",
      path: "/identities",
      body: request,
    });
  }

  get(identity: string): Promise<IdentityResponse> {
    return this.client.request<IdentityResponse>({
      method: "GET",
      path: `/identities/${encodeURIComponent(identity)}`,
    });
  }

  /**
   * Fetch and solve the relay's registration challenge (REGISTRATION_GATE=pow).
   *
   * The callbacks exist because this runs in front of a person waiting to sign
   * up: at the difficulties an operator actually sets, a silent solve is a
   * signup screen that looks broken.
   */
  async solveRegistrationChallenge(
    options: { onChallenge?: (challenge: { bits: number }) => void;
               onSolveProgress?: (attempts: number) => void;
               signal?: AbortSignal } = {},
  ): Promise<{ token: string; solution: string }> {
    const challenge = await this.client.request<{ token: string; bits: number }>({
      method: "GET",
      path: "/auth/pow?purpose=registration",
    });
    if (!challenge.token) {
      throw new PoweurError("relay_error", "malformed registration challenge");
    }
    options.onChallenge?.({ bits: challenge.bits });
    const solution = await solvePow(challenge.token, challenge.bits, {
      ...(options.onSolveProgress ? { onProgress: options.onSolveProgress } : {}),
      ...(options.signal ? { signal: options.signal } : {}),
    });
    return { token: challenge.token, solution };
  }

  /** Owner-authorized full export; returns the raw .tar.gz bytes. */
  async export(signer: Signer): Promise<Uint8Array> {
    const issuedAt = rfc3339();
    const nonce = newNonce();
    const signature = await signer.sign(
      canonicalIdentityExport(signer.identity, issuedAt, nonce),
      "base64std",
    );
    const response = await this.client.raw({
      method: "POST",
      path: `/identities/${encodeURIComponent(signer.identity)}/export`,
      body: { issued_at: issuedAt, nonce, identity_signature: signature },
    });
    if (!response.ok) {
      throw new PoweurError("relay_error", `export failed: HTTP ${response.status}`, {
        status: response.status,
        detail: await response.text(),
      });
    }
    return new Uint8Array(await response.arrayBuffer());
  }

  /**
   * Rotate the signing key. The *old* key signs the rotation so a resolver can
   * chain trust across the change; the new document must already list the old
   * key under `previous_keys` for in-flight verifiers.
   */
  async rotate(
    oldSigner: Signer,
    newKeys: { document: IdentityDocument; newPublicKey: string; encryptionPublicKey?: string },
  ): Promise<IdentityResponse> {
    const issuedAt = rfc3339();
    const nonce = newNonce();
    const signature = await oldSigner.sign(
      canonicalIdentityRotation(
        oldSigner.identity,
        stripKeyPrefix(oldSigner.publicKey),
        stripKeyPrefix(newKeys.newPublicKey),
        issuedAt,
        nonce,
      ),
      "base64std",
    );
    return this.client.request<IdentityResponse>({
      method: "POST",
      path: `/identities/${encodeURIComponent(oldSigner.identity)}/rotate`,
      body: {
        identity_document: newKeys.document,
        new_public_key: stripKeyPrefix(newKeys.newPublicKey),
        ...(newKeys.encryptionPublicKey
          ? { encryption_public_key: stripKeyPrefix(newKeys.encryptionPublicKey) }
          : {}),
        issued_at: issuedAt,
        nonce,
        rotation_signature: signature,
      },
    });
  }
}

export interface CreateIdentityOptions {
  /** Hosted registration: signed document, no DNS token. */
  hosted?: boolean;
  dnsProvider?: string;
  dnsToken?: string;
  inviteCode?: string;
  /** Pre-generated keys (e.g. from a passkey unwrap); generated when absent. */
  keys?: StoredIdentityKeys;
  /** Solve a `pow_required` registration gate and retry once. Default true. */
  solveRegistrationPow?: boolean;
  /** Told the difficulty when a relay gates registration behind proof-of-work. */
  onRegistrationChallenge?: (challenge: { bits: number }) => void;
  /** Attempts so far, so a signup screen can show work rather than a freeze. */
  onRegistrationProgress?: (attempts: number) => void;
}

export interface CreatedIdentity {
  identity: string;
  keys: StoredIdentityKeys;
  /** The master seed, present when `createIdentity` generated the keys. */
  seed?: Uint8Array;
  publicKey: string;
  encryptionPublicKey: string;
  document: IdentityDocument;
  response: IdentityResponse;
}

/**
 * Generate keys, sign the registration envelope, and register with the relay.
 * The equivalent of `poweur identity create`.
 */
export async function createIdentity(
  api: IdentityApi,
  identity: string,
  options: CreateIdentityOptions = {},
): Promise<CreatedIdentity> {
  // Without supplied keys a new seed is generated; it comes back on the
  // result and is the only copy.
  const generated = options.keys ? undefined : generateSeedIdentity(identity);
  const keys = options.keys ?? generated!.keys;
  const { signer } = signerFor(keys);
  const publicKey = toBase64url(ed25519PublicKey(keys.signingPrivateKey));
  const encryptionPublicKey = keys.encryptionPrivateKey
    ? toBase64url(x25519PublicKey(keys.encryptionPrivateKey))
    : "";

  const relayAddress = await api.relayAddress();
  const issuedAt = rfc3339();
  const nonce = newNonce();
  const identitySignature = await signer.sign(
    canonicalIdentityRegistration(
      identity,
      publicKey,
      encryptionPublicKey,
      relayAddress,
      issuedAt,
      nonce,
    ),
    "base64std",
  );

  const document = await signDocument(
    newDocument({
      identity,
      publicKey,
      ...(encryptionPublicKey ? { encryptionPublicKey } : {}),
      relay: relayAddress,
      updatedAt: issuedAt,
    }),
    signer,
  );

  const request: RegisterIdentityRequest = {
    identity,
    public_key: publicKey,
    ...(encryptionPublicKey ? { encryption_public_key: encryptionPublicKey } : {}),
    ...(options.inviteCode ? { invite_code: options.inviteCode } : {}),
    identity_document: document,
    issued_at: issuedAt,
    nonce,
    identity_signature: identitySignature,
  };
  if (!options.hosted) {
    if (!options.dnsToken) {
      throw new PoweurError(
        "invalid_argument",
        "a DNS token is required for non-hosted registration (or pass hosted: true)",
      );
    }
    request.dns_provider = options.dnsProvider ?? "cloudflare";
    request.dns_token = options.dnsToken;
  }

  let response: IdentityResponse;
  try {
    response = await api.register(request);
  } catch (error) {
    // Branch on the relay's *code*, not its prose: the message a relay sends
    // is human-facing ("hosted registration requires a solved proof-of-work
    // challenge"), so matching on "pow_required" in it never fired against a
    // real relay and the auto-solve silently did nothing.
    const relayCode = error instanceof RelayError ? error.relayCode : undefined;
    const message = error instanceof Error ? error.message : String(error);
    const gated = relayCode === "pow_required" || message.includes("pow_required");
    if (!gated || options.solveRegistrationPow === false) throw error;
    const { token, solution } = await api.solveRegistrationChallenge({
      ...(options.onRegistrationChallenge ? { onChallenge: options.onRegistrationChallenge } : {}),
      ...(options.onRegistrationProgress ? { onSolveProgress: options.onRegistrationProgress } : {}),
    });
    response = await api.register({ ...request, pow_token: token, pow_solution: solution });
  }

  return {
    identity,
    keys,
    ...(generated ? { seed: generated.seed } : {}),
    publicKey,
    encryptionPublicKey,
    document,
    response,
  };
}
