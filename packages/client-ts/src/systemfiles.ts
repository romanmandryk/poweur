/**
 * System files (EPIC-020 E20-T6) — the small documents in an identity's drive
 * under `.poweur/`:
 *
 * - `.poweur/public/`  world-readable: profile, capabilities, avatar (served
 *   at `/.well-known/poweur/<name>`);
 * - `.poweur/relay/`   written by the owner, enforced by the relay: contacts,
 *   inbox policy, analytics consent, group roster, connected apps;
 * - `.poweur/state/`   written by the relay, read by the owner: the device
 *   registry;
 * - `.poweur/private/` end-to-end encrypted records (message history, the
 *   sign-in log) — these need storage v2's drive and are not available here
 *   yet.
 *
 * Reads and writes go through the relay's owner API,
 * `/identities/{identity}/system/{path}`, authenticated with a one-shot
 * challenge signed by the identity key (or a session key). The twin of the
 * CLI's `readSysFile` / `writeSysFile`.
 */

import type { Signer } from "./crypto/keys.js";
import { PoweurError, RelayError } from "./errors.js";
import type { RelayClient } from "./http.js";
import type { DeviceListResponse, DeviceRevokeResponse } from "./types.js";

export const SYS_PUBLIC_DIR = ".poweur/public";
export const SYS_RELAY_DIR = ".poweur/relay";
export const SYS_STATE_DIR = ".poweur/state";
export const SYS_PRIVATE_DIR = ".poweur/private";

/** A document read together with the ETag a later write can pass as If-Match. */
export interface SystemFile {
  bytes: Uint8Array;
  etag: string | null;
}

/** Owner access to one identity's system files. */
export class SystemFiles {
  readonly client: RelayClient;
  readonly signer: Signer;

  constructor(client: RelayClient, signer: Signer, readonly sessionId?: string) {
    this.client = client;
    this.signer = signer;
  }

  get identity(): string {
    return this.signer.identity;
  }

  async #auth(): Promise<Record<string, string>> {
    const { challenge } = await this.client.request<{ challenge: string }>({
      method: "GET",
      path: `/auth/challenge?identity=${encodeURIComponent(this.identity)}`,
    });
    return {
      ...(this.sessionId ? { "X-Poweur-Session-Id": this.sessionId } : {}),
      "X-Poweur-Identity": this.identity,
      "X-Poweur-Challenge": challenge,
      "X-Poweur-Signature": await this.signer.sign(challenge, "base64std"),
    };
  }

  async #fetch(
    method: string,
    path: string,
    init: { body?: string | Uint8Array; headers?: Record<string, string> } = {},
  ): Promise<Response> {
    if (path.startsWith(SYS_PRIVATE_DIR + "/")) {
      throw new PoweurError("unsupported", `${path} needs the new storage (EPIC-020), which is not available yet`);
    }
    return this.client.raw({
      method,
      path: `/identities/${encodeURIComponent(this.identity)}/system/${path}`,
      ...(init.body !== undefined ? { body: init.body } : {}),
      headers: { ...(await this.#auth()), ...init.headers },
    });
  }

  static async #fail(response: Response, what: string): Promise<never> {
    const text = await response.text().catch(() => "");
    let detail = text;
    let relayCode: string | undefined;
    try {
      const parsed = JSON.parse(text) as Record<string, unknown>;
      detail = String(parsed["detail"] ?? parsed["error"] ?? text);
      if (typeof parsed["error"] === "string") relayCode = parsed["error"];
    } catch {
      // Non-JSON body: the raw text is the best detail available.
    }
    if (response.status === 412) {
      throw new PoweurError("conflict", `${what}: changed since it was read`, { status: 412, detail });
    }
    throw new RelayError(detail || `${what}: HTTP ${response.status}`, {
      status: response.status,
      detail,
      ...(relayCode !== undefined ? { relayCode } : {}),
    });
  }

  /** Read a document with its ETag, or null when it does not exist. */
  async get(path: string): Promise<SystemFile | null> {
    const response = await this.#fetch("GET", path);
    if (response.status === 404) return null;
    if (!response.ok) return SystemFiles.#fail(response, `read ${path}`);
    return { bytes: new Uint8Array(await response.arrayBuffer()), etag: response.headers.get("ETag") };
  }

  /** Read a text document, or null when it does not exist. */
  async readOptional(path: string): Promise<string | null> {
    const file = await this.get(path);
    return file ? new TextDecoder().decode(file.bytes) : null;
  }

  /**
   * Replace a document. `ifMatch` is the ETag from `get` (or "*" for "must
   * exist"); a stale one throws a `conflict` PoweurError. Returns the new ETag.
   */
  async write(path: string, body: string | Uint8Array, options: { ifMatch?: string } = {}): Promise<string | null> {
    const response = await this.#fetch("PUT", path, {
      body,
      ...(options.ifMatch ? { headers: { "If-Match": options.ifMatch } } : {}),
    });
    if (!response.ok) return SystemFiles.#fail(response, `write ${path}`);
    return response.headers.get("ETag");
  }

  async writeJson(path: string, value: unknown, options: { ifMatch?: string } = {}): Promise<string | null> {
    return this.write(path, JSON.stringify(value, null, 2) + "\n", options);
  }

  /** Remove a document; false when it did not exist. */
  async remove(path: string): Promise<boolean> {
    const response = await this.#fetch("DELETE", path);
    if (response.status === 404) return false;
    if (!response.ok) return SystemFiles.#fail(response, `remove ${path}`);
    return true;
  }
}

/**
 * The owner's device registry (`.poweur/state/devices.json`, EPIC-004 E04-T6),
 * read and revoked through `/devices/{identity}` with the same challenge
 * authentication as system files.
 */
export class DeviceRegistry {
  readonly #files: SystemFiles;

  constructor(files: SystemFiles) {
    this.#files = files;
  }

  async #auth(): Promise<Record<string, string>> {
    const { challenge } = await this.#files.client.request<{ challenge: string }>({
      method: "GET",
      path: `/auth/challenge?identity=${encodeURIComponent(this.#files.identity)}`,
    });
    return {
      ...(this.#files.sessionId ? { "X-Poweur-Session-Id": this.#files.sessionId } : {}),
      "X-Poweur-Identity": this.#files.identity,
      "X-Poweur-Challenge": challenge,
      "X-Poweur-Signature": await this.#files.signer.sign(challenge, "base64std"),
    };
  }

  /** Every device the relay has seen for this identity. */
  async list(): Promise<DeviceListResponse> {
    return this.#files.client.request<DeviceListResponse>({
      method: "GET",
      path: `/devices/${encodeURIComponent(this.#files.identity)}`,
      headers: await this.#auth(),
    });
  }

  /** Revoke one device: its sessions end and it cannot re-arm itself. */
  async revoke(deviceId: string): Promise<DeviceRevokeResponse> {
    return this.#files.client.request<DeviceRevokeResponse>({
      method: "POST",
      path: `/devices/${encodeURIComponent(this.#files.identity)}/revoke`,
      headers: await this.#auth(),
      body: { device_id: deviceId },
    });
  }
}
