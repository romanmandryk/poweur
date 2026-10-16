/**
 * The user's side of the email bridge (EPIC-023 E23-T5): opt in and out,
 * send, upload large attachments and open the bridge's blobs. Reached as
 * `client.email()`.
 */
import type { PoweurClient } from "./client.js";
import { sha256Bytes } from "./crypto/index.js";
import { rfc3339, toBase64url, randomBytes, utf8 } from "./encoding.js";
import {
  EMAIL_OPTIN_PATH,
  EMAIL_PAYLOAD_VERSION,
  MSG_TYPE_EMAIL_SEND,
  MSG_TYPE_EMAIL_SETTINGS,
  canonicalEmailUpload,
  emailAddressFor,
  emailBridgeOf,
  normalizeEmailSettings,
  hex,
  openEmailBlob,
  sealEmailUpload,
  signEmailOptIn,
  type EmailBlobRef,
  type EmailOptIn,
  type EmailSend,
  type EmailSettings,
} from "./email.js";
import { PoweurError, RelayError } from "./errors.js";
import { defaultFetch } from "./http.js";
import { INBOX_POLICY_PATH, validateInboxPolicy } from "./policy.js";
import { resolveIdentity } from "./resolve.js";
import { newThreadId } from "./subject.js";
import type { InboxPolicy } from "./types.js";

/** What `capabilities.json` advertises, as far as email goes. */
export interface EmailCapabilities {
  /** The bridge's Poweur ID (on a user's capabilities). */
  bridge?: string;
  /** The bridge's HTTPS API (on the bridge's own capabilities). */
  api?: string;
  /** False when the bridge only receives (features.email_send = "off"). */
  send?: boolean;
  /**
   * True when the bridge has paused sending for most IDs
   * (features.email_send = "paused"): sends may be refused, so say so.
   */
  sendPaused?: boolean;
}

export interface EmailState {
  enabled: boolean;
  address: string | null;
  bridge: string | null;
  optIn: EmailOptIn | null;
  /** On by default: no opt-in of our own, the relay's bridge. */
  byDefault?: boolean;
}

export type EmailDraft = Omit<EmailSend, "version" | "send_id"> & { send_id?: string };

export class EmailApi {
  readonly #client: PoweurClient;

  constructor(client: PoweurClient) {
    this.#client = client;
  }

  #scheme(): "http" | "https" {
    return this.#client.relay.relayUrl.startsWith("http://") ? "http" : "https";
  }

  /** The `endpoints` of an identity's capabilities.json; {} when absent. */
  async capabilities(identity: string): Promise<EmailCapabilities> {
    const fetchImpl = this.#client.resolveOptions.fetch ?? defaultFetch();
    try {
      const response = await fetchImpl(`${this.#scheme()}://${identity.trim().toLowerCase()}/.well-known/poweur/capabilities.json`);
      if (!response.ok) return {};
      const caps = (await response.json()) as { endpoints?: Record<string, string>; features?: Record<string, string> };
      const endpoints = caps.endpoints ?? {};
      return {
        ...(caps.features?.["email_send"] ? { send: caps.features["email_send"] !== "off" } : {}),
        ...(caps.features?.["email_send"] === "paused" ? { sendPaused: true } : {}),
        ...(endpoints["email_bridge"] ? { bridge: endpoints["email_bridge"] } : {}),
        ...(endpoints["email_api"] ? { api: endpoints["email_api"].replace(/\/+$/, "") } : {}),
      };
    } catch {
      return {};
    }
  }

  /** The bridge our relay offers, if any. */
  async offeredBridge(): Promise<string | null> {
    return (await this.capabilities(this.#client.signer.identity)).bridge ?? null;
  }

  /**
   * Whether email is on, and through which bridge. Email is on by default
   * through the bridge our relay offers, until the policy opts out (or names
   * another bridge, which then needs our signed email.json). Pass `offered`
   * when it is already known, to save a fetch.
   */
  async state(offered?: string | null): Promise<EmailState> {
    const raw = await this.#client.system().readOptional(EMAIL_OPTIN_PATH);
    let optIn: EmailOptIn | null = null;
    try {
      optIn = raw ? (JSON.parse(raw) as EmailOptIn) : null;
    } catch {
      optIn = null;
    }
    const { policy } = await this.#client.policy();
    const fallback = offered === undefined ? await this.offeredBridge() : offered;
    const effective = emailBridgeOf(policy, fallback);
    const bridge = effective || policy.email?.bridge || optIn?.bridge || null;
    if (effective && optIn && optIn.bridge === effective) return { enabled: true, address: optIn.address, bridge, optIn };
    if (effective && !optIn && !policy.email) {
      try {
        return { enabled: true, address: emailAddressFor(this.#client.signer.identity), bridge, optIn: null, byDefault: true };
      } catch {
        // A name with no address (not name.domain) has no email.
      }
    }
    return { enabled: false, address: optIn?.address ?? null, bridge, optIn };
  }

  async #writePolicy(change: (policy: InboxPolicy) => void): Promise<InboxPolicy> {
    const { policy } = await this.#client.policy();
    const next: InboxPolicy = { ...policy, version: 1 };
    change(next);
    validateInboxPolicy(next);
    await this.#client.system().writeJson(INBOX_POLICY_PATH, next);
    return next;
  }

  /**
   * Opt in: publish the signed email.json and let the bridge through the
   * inbox policy. `bridge` defaults to the one our relay advertises.
   */
  async enable(bridge?: string): Promise<EmailState> {
    const chosen = (bridge ?? (await this.offeredBridge()) ?? "").trim().toLowerCase();
    if (!chosen) throw new PoweurError("not_found", "this relay offers no email bridge");
    // Policy first: once email.json is public the bridge may deliver at once.
    await this.#writePolicy((policy) => {
      policy.email = { allow: true, bridge: chosen };
    });
    const optIn = await signEmailOptIn(this.#client.signer, chosen);
    await this.#client.system().writeJson(EMAIL_OPTIN_PATH, optIn);
    return { enabled: true, address: optIn.address, bridge: chosen, optIn };
  }

  /**
   * Opt out: withdraw email.json (the bridge refuses mail within its cache)
   * and close the policy — always written, since no email block means "on".
   */
  async disable(): Promise<EmailState> {
    await this.#client.system().remove(EMAIL_OPTIN_PATH);
    const policy = await this.#writePolicy((p) => {
      p.email = { allow: false, ...(p.email?.bridge ? { bridge: p.email.bridge } : {}) };
    });
    return { enabled: false, address: null, bridge: policy.email?.bridge ?? null, optIn: null };
  }

  async #bridge(): Promise<string> {
    const { policy } = await this.#client.policy();
    const bridge = emailBridgeOf(policy, policy.email ? null : await this.offeredBridge());
    if (!bridge) throw new PoweurError("invalid_argument", "email is not enabled for this identity");
    return bridge;
  }

  /**
   * Send our receive filters to the bridge (`email.settings`). The archived
   * copy is our record of them: read the newest from the archive.
   */
  async saveSettings(settings: Omit<EmailSettings, "version" | "updated_at">, options: { signWith?: "session" | "identity" } = {}) {
    const bridge = await this.#bridge();
    const payload = normalizeEmailSettings({ ...settings, version: EMAIL_PAYLOAD_VERSION, updated_at: rfc3339(new Date()) });
    const result = await this.#client.sendAndArchive(bridge, JSON.stringify(payload), {
      type: MSG_TYPE_EMAIL_SETTINGS,
      signWith: options.signWith ?? "session",
    });
    return { ...result, payload };
  }

  /**
   * Ask the bridge to send an email. The send is archived like any message
   * we send, so it shows among sent mail. Returns the send id the bridge's
   * `email.status` messages will name.
   */
  async send(draft: EmailDraft, options: { signWith?: "session" | "identity" } = {}) {
    const bridge = await this.#bridge();
    const payload: EmailSend = { ...draft, version: EMAIL_PAYLOAD_VERSION, send_id: draft.send_id || newThreadId() };
    const result = await this.#client.sendAndArchive(bridge, JSON.stringify(payload), {
      type: MSG_TYPE_EMAIL_SEND,
      signWith: options.signWith ?? "session",
    });
    return { ...result, sendId: payload.send_id, payload };
  }

  /**
   * Upload one outbound attachment too large to inline, sealed to the
   * bridge. Returns the upload id to put in `attachments[].upload`.
   */
  async upload(bytes: Uint8Array): Promise<{ upload: string; sha256: string; size: number }> {
    const bridge = await this.#bridge();
    const { api } = await this.capabilities(bridge);
    if (!api) throw new PoweurError("not_found", `${bridge} does not advertise an upload endpoint`);
    const resolved = await resolveIdentity(bridge, this.#client.resolveOptions);
    const encryptionKey = resolved.document.encryption_public_key;
    if (!encryptionKey) throw new PoweurError("not_found", `${bridge} has no published encryption key`);
    const uploadId = newThreadId();
    const body = utf8(sealEmailUpload(encryptionKey.replace(/^x25519:/, ""), bytes, uploadId));
    const digest = hex(sha256Bytes(body));
    const issuedAt = rfc3339();
    const nonce = toBase64url(randomBytes(16));
    const identity = this.#client.signer.identity.toLowerCase();
    const signature = await this.#client.signer.sign(canonicalEmailUpload(identity, uploadId, digest, body.length, issuedAt, nonce), "base64url");
    const fetchImpl = this.#client.resolveOptions.fetch ?? defaultFetch();
    const response = await fetchImpl(`${api}/v1/uploads/${uploadId}`, {
      method: "PUT",
      body: body as BodyInit,
      headers: {
        "Content-Type": "application/json",
        "X-Poweur-Identity": identity,
        "X-Poweur-Timestamp": issuedAt,
        "X-Poweur-Nonce": nonce,
        "X-Poweur-Signature": signature,
      },
    });
    if (!response.ok) {
      const detail = await response.text().catch(() => "");
      throw new RelayError(`upload refused: ${detail || response.status}`, { status: response.status, detail });
    }
    return { upload: uploadId, sha256: hex(sha256Bytes(bytes)), size: bytes.length };
  }

  /** Download and open a blob (large attachment or original message). */
  async blob(ref: EmailBlobRef): Promise<Uint8Array> {
    if (!this.#client.decryptor) throw new PoweurError("invalid_argument", "opening email needs a decryptor");
    return openEmailBlob(this.#client.decryptor, ref, this.#client.resolveOptions.fetch ?? defaultFetch());
  }
}

