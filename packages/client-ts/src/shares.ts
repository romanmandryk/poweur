/**
 * Share grants and groups — the twin of `poweur share`.
 *
 * A grant is a document the owner signs and stores in their *own* tree at
 * `poweur-sys/relay/shares/<share-id>.json`. The relay enforces grants but
 * cannot forge them, and revocation is simply deleting the file. Groups live
 * alongside at `poweur-sys/relay/groups/<name>.json`.
 */

import { canonicalShareGrant, canonicalShareGroup, normalizeGrantPath } from "./canonical.js";
import { parseEd25519PublicKey, verifyCanonical } from "./crypto/index.js";
import type { Signer } from "./crypto/keys.js";
import { rfc3339 } from "./encoding.js";
import { PoweurError } from "./errors.js";
import type { DavClient } from "./files.js";
import { newShareId } from "./ids.js";
import type { SyncClient } from "./sync.js";
import { PERM_READ, PERM_WRITE, type ShareAudience, type ShareGrant, type ShareGroup } from "./types.js";

export { normalizeGrantPath };

export const SHARES_DIR = "poweur-sys/relay/shares";
export const GROUPS_DIR = "poweur-sys/relay/groups";

export const MAX_GRANT_AUDIENCE = 100;
export const MAX_GROUP_MEMBERS = 1000;

/** Structural validation — everything except the signature. */
export function validateGrant(grant: Omit<ShareGrant, "signature">): void {
  if (!grant.share_id?.trim()) throw new PoweurError("invalid_argument", "share_id is required");
  if (!grant.owner?.trim()) throw new PoweurError("invalid_argument", "owner is required");
  normalizeGrantPath(grant.path);
  if (!grant.audience?.length) throw new PoweurError("invalid_argument", "audience is empty");
  if (grant.audience.length > MAX_GRANT_AUDIENCE) {
    throw new PoweurError("invalid_argument", `audience exceeds ${MAX_GRANT_AUDIENCE} entries`);
  }
  for (const entry of grant.audience) {
    const hasId = Boolean(entry.id?.trim());
    const hasGroup = Boolean(entry.group?.trim());
    if (hasId === hasGroup) {
      throw new PoweurError(
        "invalid_argument",
        "each audience entry needs exactly one of id or group",
      );
    }
  }
  if (!grant.permissions?.length) {
    throw new PoweurError("invalid_argument", "permissions is empty");
  }
  for (const permission of grant.permissions) {
    if (permission !== PERM_READ && permission !== PERM_WRITE) {
      throw new PoweurError(
        "invalid_argument",
        `unknown permission "${permission}" (v1 vocabulary: read, write)`,
      );
    }
  }
}

export function grantExpired(grant: ShareGrant, now: Date = new Date()): boolean {
  if (!grant.expires_at) return false;
  const expiry = Date.parse(grant.expires_at);
  // An unparseable expiry fails closed.
  return Number.isNaN(expiry) || now.getTime() > expiry;
}

export function grantAllowsWrite(grant: ShareGrant): boolean {
  return grant.permissions.includes(PERM_WRITE);
}

export function verifyGrantSignature(grant: ShareGrant, ownerPublicKey: string): boolean {
  return verifyCanonical(
    parseEd25519PublicKey(ownerPublicKey),
    canonicalShareGrant(grant),
    grant.signature,
  );
}

export function verifyGroupSignature(group: ShareGroup, ownerPublicKey: string): boolean {
  return verifyCanonical(
    parseEd25519PublicKey(ownerPublicKey),
    canonicalShareGroup(group),
    group.signature,
  );
}

export interface CreateShareOptions {
  /** Direct recipients by Poweur ID. */
  with?: string[];
  /** Owner-local group names. */
  withGroups?: string[];
  permissions?: "read" | "rw";
  expiresAt?: string;
  shareId?: string;
}

/** Build and sign a grant without writing it — useful for offline flows. */
export async function buildGrant(
  signer: Signer,
  path: string,
  options: CreateShareOptions = {},
): Promise<ShareGrant> {
  const audience: ShareAudience[] = [
    ...(options.with ?? []).map((id) => ({ id })),
    ...(options.withGroups ?? []).map((group) => ({ group })),
  ];
  if (audience.length === 0) {
    throw new PoweurError("invalid_argument", "at least one recipient or group is required");
  }
  const permissions =
    options.permissions === "rw" ? [PERM_READ, PERM_WRITE] : [PERM_READ];
  const draft = {
    share_id: options.shareId ?? newShareId(),
    owner: signer.identity,
    path: normalizeGrantPath(path),
    audience,
    permissions,
    created_at: rfc3339(),
    ...(options.expiresAt ? { expires_at: options.expiresAt } : {}),
  };
  validateGrant(draft);
  const signature = await signer.sign(canonicalShareGrant(draft), "base64url");
  return { ...draft, signature };
}

/** CRUD over an owner's grant and group documents. */
export class Shares {
  readonly #dav: DavClient;
  readonly #sync: SyncClient;

  constructor(dav: DavClient, sync: SyncClient) {
    this.#dav = dav;
    this.#sync = sync;
  }

  /** Create, sign and store a grant (`poweur share add`). */
  async add(signer: Signer, path: string, options: CreateShareOptions = {}): Promise<ShareGrant> {
    const grant = await buildGrant(signer, path, options);
    await this.#dav.writeJson(`${SHARES_DIR}/${grant.share_id}.json`, grant);
    return grant;
  }

  /**
   * List grants. The manifest endpoint gives the file names — a PROPFIND on
   * poweur-sys would work too, but the manifest is one request for the whole
   * prefix and is what the CLI uses.
   */
  async list(): Promise<ShareGrant[]> {
    return this.#loadAll<ShareGrant>(SHARES_DIR);
  }

  /** Revoke by deleting the grant file. Returns false if it wasn't there. */
  async revoke(shareId: string): Promise<boolean> {
    if (/[/\\]/.test(shareId)) {
      throw new PoweurError("invalid_argument", "invalid share id");
    }
    return this.#dav.remove(`${SHARES_DIR}/${shareId}.json`);
  }

  /** Create or replace a group (`poweur share group set`). */
  async setGroup(signer: Signer, name: string, members: string[]): Promise<ShareGroup> {
    if (/[/\\]/.test(name)) {
      throw new PoweurError("invalid_argument", "group name must not contain slashes");
    }
    if (members.length > MAX_GROUP_MEMBERS) {
      throw new PoweurError("invalid_argument", `group exceeds ${MAX_GROUP_MEMBERS} members`);
    }
    const draft = {
      group: name,
      owner: signer.identity,
      members,
      updated_at: rfc3339(),
    };
    const signature = await signer.sign(canonicalShareGroup(draft), "base64url");
    const group: ShareGroup = { ...draft, signature };
    await this.#dav.writeJson(`${GROUPS_DIR}/${name}.json`, group);
    return group;
  }

  async listGroups(): Promise<ShareGroup[]> {
    return this.#loadAll<ShareGroup>(GROUPS_DIR);
  }

  async removeGroup(name: string): Promise<boolean> {
    if (/[/\\]/.test(name)) {
      throw new PoweurError("invalid_argument", "invalid group name");
    }
    return this.#dav.remove(`${GROUPS_DIR}/${name}.json`);
  }

  async #loadAll<T>(directory: string): Promise<T[]> {
    let names: string[];
    try {
      const { entries } = await this.#sync.manifest([directory]);
      names = entries
        .filter((e) => !e.dir && e.path.startsWith(`${directory}/`) && e.path.endsWith(".json"))
        .map((e) => e.path.slice(directory.length + 1));
    } catch {
      // A relay without the manifest endpoint (or an empty tree) still lists
      // via PROPFIND; an absent directory just means no documents.
      try {
        names = (await this.#dav.list(directory))
          .filter((e) => !e.dir && e.name.endsWith(".json"))
          .map((e) => e.name);
      } catch {
        return [];
      }
    }
    const out: T[] = [];
    for (const name of names) {
      const raw = await this.#dav.readOptional(`${directory}/${name}`);
      if (!raw) continue;
      try {
        out.push(JSON.parse(raw) as T);
      } catch {
        // A malformed document is skipped rather than failing the whole list.
      }
    }
    return out;
  }
}
