/**
 * Share grants and groups — the twin of `poweur share`.
 *
 * A grant is a document the owner signs and stores in their *own* tree at
 * `poweur-sys/relay/shares/<share-id>.json`. The relay enforces grants but
 * cannot forge them, and revocation is simply deleting the file. Groups live
 * alongside at `poweur-sys/relay/groups/<name>.json`.
 */

import { hashAppPassword } from "./apppass.js";
import { canonicalShareGrant, canonicalShareGroup, normalizeGrantPath } from "./canonical.js";
import { parseEd25519PublicKey, verifyCanonical } from "./crypto/index.js";
import type { Signer } from "./crypto/keys.js";
import { randomBytes, rfc3339 } from "./encoding.js";
import { PoweurError, RelayError } from "./errors.js";
import type { DavClient } from "./files.js";
import { newShareId } from "./ids.js";
import { validateIdentityName } from "./names.js";
import type { SyncClient } from "./sync.js";
import {
  PERM_READ,
  PERM_CREATE,
  PERM_WRITE,
  type ShareAudience,
  type ShareGrant,
  type ShareClaim,
  type ShareGroup,
  type ShareLink,
  type ShareMount,
  type ShareOffer,
} from "./types.js";

export { normalizeGrantPath };

export const SHARES_DIR = "poweur-sys/relay/shares";
export const GROUPS_DIR = "poweur-sys/relay/groups";
export const SHARE_MOUNT_FILE = ".poweur-mount.json";
export const SHARE_LIFECYCLE_VERSION = 1;
export const MAX_SHARE_MOUNT_NAME = 128;

export const MAX_GRANT_AUDIENCE = 100;
export const MAX_GROUP_MEMBERS = 1000;

/**
 * Link-share tokens (E05-T4) — 16 random bytes as lowercase unpadded
 * base32: 26 characters of [a-z2-7], 128 bits of entropy, URL-path safe and
 * free of look-alike characters. Mirrors identity.GenerateLinkToken.
 */
export const LINK_TOKEN_BYTES = 16;
export const LINK_TOKEN_LEN = 26;
export const MAX_LINK_DOWNLOADS = 1_000_000;

const LINK_ALPHABET = "abcdefghijklmnopqrstuvwxyz234567";

/** A fresh capability-URL token. */
export function generateLinkToken(): string {
  // 16 bytes → 26 base32 characters, emitted five bits at a time so the
  // result matches Go's encoder byte for byte.
  const bytes = randomBytes(LINK_TOKEN_BYTES);
  let out = "";
  let buffer = 0;
  let bits = 0;
  for (const byte of bytes) {
    buffer = (buffer << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += LINK_ALPHABET[(buffer >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += LINK_ALPHABET[(buffer << (5 - bits)) & 31];
  return out;
}

/** Throws unless `token` has the exact shape a link grant may carry. */
export function validateLinkToken(token: string): void {
  if (token.length !== LINK_TOKEN_LEN) {
    throw new PoweurError("invalid_argument", `link token must be ${LINK_TOKEN_LEN} characters`);
  }
  for (const character of token) {
    if (!LINK_ALPHABET.includes(character)) {
      throw new PoweurError(
        "invalid_argument",
        `link token contains an invalid character "${character}"`,
      );
    }
  }
}

/** The grant's capability token, or undefined when it is not a link grant. */
export function linkToken(grant: Pick<ShareGrant, "audience">): string | undefined {
  for (const entry of grant.audience ?? []) {
    const token = entry.link?.trim();
    if (token) return token.toLowerCase();
  }
  return undefined;
}

export function grantIsLink(grant: Pick<ShareGrant, "audience">): boolean {
  return linkToken(grant) !== undefined;
}

/** Structural validation — everything except the signature. */
export function validateGrant(grant: Omit<ShareGrant, "signature">): void {
  if (!grant.share_id?.trim()) throw new PoweurError("invalid_argument", "share_id is required");
  if (grant.source_share_id) {
    assertShareId(grant.source_share_id);
    if (grant.source_share_id.trim() !== grant.source_share_id) {
      throw new PoweurError("invalid_argument", "source_share_id must not contain surrounding whitespace");
    }
    if (grant.source_share_id === grant.share_id) {
      throw new PoweurError("invalid_argument", "source_share_id must differ from share_id");
    }
  }
  if (!grant.owner?.trim()) throw new PoweurError("invalid_argument", "owner is required");
  normalizeGrantPath(grant.path);
  if (!grant.audience?.length) throw new PoweurError("invalid_argument", "audience is empty");
  if (grant.audience.length > MAX_GRANT_AUDIENCE) {
    throw new PoweurError("invalid_argument", `audience exceeds ${MAX_GRANT_AUDIENCE} entries`);
  }
  let links = 0;
  for (const entry of grant.audience) {
    const set = [entry.id, entry.group, entry.link].filter((v) => Boolean(v?.trim())).length;
    if (set !== 1) {
      throw new PoweurError(
        "invalid_argument",
        "each audience entry needs exactly one of id, group or link",
      );
    }
    const token = entry.link?.trim();
    if (token) {
      links += 1;
      validateLinkToken(token);
    }
  }
  validateGrantLink(grant, links);
  if (grant.source_share_id && (links !== 0 || grant.audience.length !== 1 || !grant.audience[0]?.id?.trim())) {
    throw new PoweurError(
      "invalid_argument",
      "source_share_id requires exactly one direct identity recipient",
    );
  }
  if (!grant.permissions?.length) {
    throw new PoweurError("invalid_argument", "permissions is empty");
  }
  for (const permission of grant.permissions) {
    if (permission !== PERM_READ && permission !== PERM_WRITE && permission !== PERM_CREATE) {
      throw new PoweurError(
        "invalid_argument",
        `unknown permission "${permission}" (v1 vocabulary: read, write, create)`,
      );
    }
    if (permission === PERM_CREATE && !grant.link?.file_request) {
      throw new PoweurError("invalid_argument", "create is currently limited to file-request links");
    }
  }
}

/**
 * The link-share rules (identity.validateLink). A link grant is a
 * *capability*: whoever holds the URL is the audience. v1 therefore keeps it
 * narrow — one token per grant, never mixed with identity or group entries.
 * Ordinary links are read-only; explicitly marked file requests are the
 * create-only subset and cannot inspect existing objects.
 */
function validateGrantLink(grant: Omit<ShareGrant, "signature">, links: number): void {
  if (links > 1) {
    throw new PoweurError("invalid_argument", "a grant carries at most one link token");
  }
  if (links === 1) {
    if (grant.audience.length !== 1) {
      throw new PoweurError(
        "invalid_argument",
        "a link grant's audience is the link alone (no ids or groups)",
      );
    }
    if (grant.permissions?.includes(PERM_WRITE)) {
      throw new PoweurError(
        "invalid_argument",
        "link shares are read-only or create-only and cannot grant write",
      );
    }
  } else if (grant.link) {
    throw new PoweurError("invalid_argument", "link options require a link audience entry");
  }
  if (!grant.link) return;
  if (grant.link.password && !grant.link.password.startsWith("$argon2id$")) {
    throw new PoweurError(
      "invalid_argument",
      "link password must be a PHC argon2id hash, never a plaintext password",
    );
  }
  const max = grant.link.max_downloads ?? 0;
  if (max < 0 || max > MAX_LINK_DOWNLOADS) {
    throw new PoweurError(
      "invalid_argument",
      `max_downloads must be between 0 (unlimited) and ${MAX_LINK_DOWNLOADS}`,
    );
  }
  const request = grant.link.file_request;
  if (!request) {
    if (grant.permissions.length !== 1 || grant.permissions[0] !== PERM_READ) {
      throw new PoweurError("invalid_argument", "download links require exactly the read permission");
    }
    return;
  }
  if (grant.permissions.length !== 1 || grant.permissions[0] !== PERM_CREATE) {
    throw new PoweurError("invalid_argument", "file-request links require exactly the create permission");
  }
  if (max !== 0) {
    throw new PoweurError("invalid_argument", "file-request links cannot set max_downloads");
  }
  if ((request.max_uploads ?? 0) < 0 || (request.max_uploads ?? 0) > MAX_LINK_DOWNLOADS) {
    throw new PoweurError(
      "invalid_argument",
      `max_uploads must be between 0 (unlimited) and ${MAX_LINK_DOWNLOADS}`,
    );
  }
  if ((request.max_bytes ?? 0) < 0 || (request.max_object_bytes ?? 0) < 0) {
    throw new PoweurError("invalid_argument", "file-request byte limits cannot be negative");
  }
  for (const mediaType of request.allowed_types ?? []) {
    const normalized = mediaType.trim().toLowerCase();
    if (!normalized || /[\s;]/.test(normalized) || !normalized.includes("/")) {
      throw new PoweurError("invalid_argument", `invalid allowed media type "${mediaType}"`);
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

export function validateShareClaim(claim: ShareClaim): void {
  if (claim?.version !== SHARE_LIFECYCLE_VERSION) {
    throw new PoweurError("invalid_argument", `unsupported share claim version ${String(claim?.version)}`);
  }
  if (!claim.share_id?.trim() || /[/\\]/.test(claim.share_id) || claim.share_id === "." || claim.share_id === "..") {
    throw new PoweurError("invalid_argument", "invalid share_id");
  }
  validateLinkToken(claim.token.trim().toLowerCase());
  for (const [label, value] of [["owner", claim.owner], ["claimant", claim.claimant]] as const) {
    const normalized = value?.trim().toLowerCase();
    try { validateIdentityName(normalized); } catch { throw new PoweurError("invalid_argument", `invalid ${label}`); }
  }
  if (!["viewed", "downloaded", "uploaded"].includes(claim.action)) {
    throw new PoweurError("invalid_argument", `invalid claim action "${claim.action}"`);
  }
  assertTimestamp(claim.claimed_at, "claimed_at");
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

/** Build the encrypted body of one `sys.share.offer`. */
export function buildShareOffer(grant: ShareGrant, offeredAt = rfc3339()): ShareOffer {
  validateGrant(grant);
  if (!grant.signature) throw new PoweurError("invalid_argument", "offered grant is unsigned");
  if (grantIsLink(grant)) {
    throw new PoweurError("invalid_argument", "link grants are not sent as identity offers");
  }
  assertTimestamp(offeredAt, "offered_at");
  return { version: SHARE_LIFECYCLE_VERSION, grant, offered_at: offeredAt };
}

/** Validate a decrypted offer before presenting or mounting it. */
export function validateShareOffer(
  offer: ShareOffer,
  recipient: string,
  ownerPublicKey?: string,
): void {
  if (offer?.version !== SHARE_LIFECYCLE_VERSION) {
    throw new PoweurError("invalid_argument", `unsupported share offer version ${String(offer?.version)}`);
  }
  validateGrant(offer.grant);
  if (!offer.grant.signature) throw new PoweurError("invalid_argument", "offered grant is unsigned");
  if (grantIsLink(offer.grant)) {
    throw new PoweurError("invalid_argument", "link grants are not sent as identity offers");
  }
  assertTimestamp(offer.offered_at, "offered_at");
  const target = recipient.trim().toLowerCase();
  if (!target || !offer.grant.audience.some((entry) => entry.id?.trim().toLowerCase() === target)) {
    throw new PoweurError("policy_rejected", `${recipient} is not a direct audience member`);
  }
  if (ownerPublicKey && !verifyGrantSignature(offer.grant, ownerPublicKey)) {
    throw new PoweurError("invalid_signature", "offered grant signature verification failed");
  }
}

/** Safe display/directory name derived from the last segment of a grant path. */
export function defaultShareMountName(sourcePath: string): string {
  const normalized = normalizeGrantPath(sourcePath);
  const leaf = normalized.split("/").pop() ?? "share";
  const safe = leaf
    .normalize("NFKC")
    .replace(/[\\/\u0000-\u001f\u007f]/g, "-")
    .replace(/^\.+|\.+$/g, "")
    .trim()
    .slice(0, MAX_SHARE_MOUNT_NAME);
  return safe || "share";
}

/** Validate and normalize `shared/<owner>/<name>`. */
export function normalizeShareMountPath(raw: string, owner: string): string {
  const value = raw.trim().replace(/^\/+|\/+$/g, "");
  const parts = value.split("/");
  if (
    parts.length !== 3 ||
    parts[0] !== "shared" ||
    parts[1]?.toLowerCase() !== owner.trim().toLowerCase() ||
    !parts[2] ||
    parts[2] === "." ||
    parts[2] === ".." ||
    parts[2].length > MAX_SHARE_MOUNT_NAME ||
    /[\\\u0000-\u001f\u007f]/.test(parts[2])
  ) {
    throw new PoweurError("invalid_argument", "mount_path must be shared/<owner>/<safe-name>");
  }
  return value;
}

export function validateShareMount(mount: ShareMount): void {
  if (mount?.version !== SHARE_LIFECYCLE_VERSION) {
    throw new PoweurError("invalid_argument", `unsupported share mount version ${String(mount?.version)}`);
  }
  assertShareId(mount.share_id);
  if (!mount.owner?.trim()) throw new PoweurError("invalid_argument", "owner is required");
  normalizeGrantPath(mount.source_path);
  if (!mount.permissions?.length) throw new PoweurError("invalid_argument", "permissions is empty");
  for (const permission of mount.permissions) {
    if (permission !== PERM_READ && permission !== PERM_WRITE) {
      throw new PoweurError("invalid_argument", `unknown permission "${permission}"`);
    }
  }
  assertTimestamp(mount.accepted_at, "accepted_at");
  if (mount.expires_at) assertTimestamp(mount.expires_at, "expires_at");
}

function assertTimestamp(value: string, field: string): void {
  if (!value || Number.isNaN(Date.parse(value))) {
    throw new PoweurError("invalid_argument", `${field} must be RFC3339`);
  }
}

function assertShareId(shareId: string): void {
  if (!shareId?.trim() || /[/\\]/.test(shareId) || shareId === "." || shareId === "..") {
    throw new PoweurError("invalid_argument", "invalid share_id");
  }
}

export interface CreateShareOptions {
  /** Direct recipients by Poweur ID. */
  with?: string[];
  /** Owner-local group names. */
  withGroups?: string[];
  permissions?: "read" | "rw";
  expiresAt?: string;
  shareId?: string;
  /** Public capability this single-recipient direct grant upgrades. */
  sourceShareId?: string;
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
    ...(options.sourceShareId ? { source_share_id: options.sourceShareId } : {}),
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

export interface CreateLinkShareOptions {
  /** Plaintext password; hashed with argon2id before it enters the grant. */
  password?: string;
  /** Cap on successful downloads; 0/absent = unlimited. */
  maxDownloads?: number;
  expiresAt?: string;
  shareId?: string;
  /** Supply the token (tests, or re-issuing a known link). */
  token?: string;
}

export interface CreateFileRequestOptions {
  password?: string;
  maxUploads?: number;
  maxBytes?: number;
  maxObjectBytes?: number;
  allowedTypes?: string[];
  notify?: boolean;
  expiresAt?: string;
  shareId?: string;
  token?: string;
}

/**
 * Build and sign a public-link grant (E05-T4) without writing it. The
 * returned token is the whole credential: it appears nowhere else, so a
 * caller that loses it has to issue a new link.
 */
export async function buildLinkGrant(
  signer: Signer,
  path: string,
  options: CreateLinkShareOptions = {},
): Promise<{ grant: ShareGrant; token: string }> {
  const token = (options.token ?? generateLinkToken()).toLowerCase();
  validateLinkToken(token);
  const link: ShareLink = {};
  if (options.password) link.password = await hashAppPassword(options.password);
  if (options.maxDownloads) link.max_downloads = options.maxDownloads;
  const draft = {
    share_id: options.shareId ?? newShareId(),
    owner: signer.identity,
    path: normalizeGrantPath(path),
    audience: [{ link: token }],
    permissions: [PERM_READ],
    created_at: rfc3339(),
    ...(options.expiresAt ? { expires_at: options.expiresAt } : {}),
    ...(link.password || link.max_downloads ? { link } : {}),
  };
  validateGrant(draft);
  const signature = await signer.sign(canonicalShareGrant(draft), "base64url");
  return { grant: { ...draft, signature }, token };
}

/** Build and sign an upload-only public capability. */
export async function buildFileRequestGrant(
  signer: Signer,
  path: string,
  options: CreateFileRequestOptions = {},
): Promise<{ grant: ShareGrant; token: string }> {
  const token = (options.token ?? generateLinkToken()).toLowerCase();
  validateLinkToken(token);
  const link: ShareLink = {
    file_request: {
      ...(options.maxUploads ? { max_uploads: options.maxUploads } : {}),
      ...(options.maxBytes ? { max_bytes: options.maxBytes } : {}),
      ...(options.maxObjectBytes ? { max_object_bytes: options.maxObjectBytes } : {}),
      ...(options.allowedTypes?.length ? { allowed_types: options.allowedTypes } : {}),
      ...(options.notify ? { notify: true } : {}),
    },
  };
  if (options.password) link.password = await hashAppPassword(options.password);
  const draft = {
    share_id: options.shareId ?? newShareId(),
    owner: signer.identity,
    path: normalizeGrantPath(path),
    audience: [{ link: token }],
    permissions: [PERM_CREATE],
    created_at: rfc3339(),
    ...(options.expiresAt ? { expires_at: options.expiresAt } : {}),
    link,
  };
  validateGrant(draft);
  const signature = await signer.sign(canonicalShareGrant(draft), "base64url");
  return { grant: { ...draft, signature }, token };
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
   * Create, sign and store a public-link grant, returning the grant and the
   * capability token to put in `https://<owner>/s/<token>` (E05-T4).
   */
  async addLink(
    signer: Signer,
    path: string,
    options: CreateLinkShareOptions = {},
  ): Promise<{ grant: ShareGrant; token: string }> {
    const built = await buildLinkGrant(signer, path, options);
    await this.#dav.writeJson(`${SHARES_DIR}/${built.grant.share_id}.json`, built.grant);
    return built;
  }

  /** Create and store an upload-only file request. */
  async addFileRequest(
    signer: Signer,
    path: string,
    options: CreateFileRequestOptions = {},
  ): Promise<{ grant: ShareGrant; token: string }> {
    const built = await buildFileRequestGrant(signer, path, options);
    await this.#dav.writeJson(`${SHARES_DIR}/${built.grant.share_id}.json`, built.grant);
    return built;
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
    assertShareId(shareId);
    return this.#dav.remove(`${SHARES_DIR}/${shareId}.json`);
  }

  /**
   * Materialize an accepted offer as a client-direct pointer in our own tree.
   * The caller must resolve the owner's current public key and pass it here;
   * accepting unverified grant bytes would turn a message into an authority.
   */
  async acceptOffer(
    offer: ShareOffer,
    recipient: string,
    ownerPublicKey: string,
    options: { name?: string; acceptedAt?: string } = {},
  ): Promise<{ mount: ShareMount; mountPath: string }> {
    validateShareOffer(offer, recipient, ownerPublicKey);
    const acceptedAt = options.acceptedAt ?? rfc3339();
    assertTimestamp(acceptedAt, "accepted_at");
    const name = options.name?.trim() || defaultShareMountName(offer.grant.path);
    const mountPath = normalizeShareMountPath(`shared/${offer.grant.owner}/${name}`, offer.grant.owner);
    const mount: ShareMount = {
      version: SHARE_LIFECYCLE_VERSION,
      share_id: offer.grant.share_id,
      owner: offer.grant.owner,
      source_path: normalizeGrantPath(offer.grant.path),
      permissions: [...offer.grant.permissions],
      accepted_at: acceptedAt,
      ...(offer.grant.expires_at ? { expires_at: offer.grant.expires_at } : {}),
    };
    validateShareMount(mount);
    await this.#ensureCollection(`shared/${offer.grant.owner}`);
    await this.#ensureCollection(mountPath);
    const documentPath = `${mountPath}/${SHARE_MOUNT_FILE}`;
    const existing = await this.#dav.readOptional(documentPath);
    if (existing) {
      try {
        const previous = JSON.parse(existing) as ShareMount;
        if (previous.share_id !== mount.share_id) {
          throw new PoweurError("conflict", `mount path ${mountPath} already belongs to another share`);
        }
      } catch (error) {
        if (error instanceof PoweurError) throw error;
        throw new PoweurError("conflict", `mount path ${mountPath} contains an invalid pointer`);
      }
    }
    await this.#dav.writeJson(documentPath, mount);
    return { mount, mountPath };
  }

  /** List all valid recipient-local mount pointers. */
  async listMounts(): Promise<Array<{ mount: ShareMount; mountPath: string }>> {
    let entries;
    try {
      ({ entries } = await this.#sync.manifest(["shared"]));
    } catch {
      return [];
    }
    const mounts: Array<{ mount: ShareMount; mountPath: string }> = [];
    for (const entry of entries) {
      if (entry.dir || !entry.path.endsWith(`/${SHARE_MOUNT_FILE}`)) continue;
      const raw = await this.#dav.readOptional(entry.path);
      if (!raw) continue;
      try {
        const mount = JSON.parse(raw) as ShareMount;
        validateShareMount(mount);
        const mountPath = entry.path.slice(0, -(`/${SHARE_MOUNT_FILE}`.length));
        normalizeShareMountPath(mountPath, mount.owner);
        mounts.push({ mount, mountPath });
      } catch {
        // A forged/malformed pointer grants no authority and is omitted.
      }
    }
    return mounts;
  }

  async removeMount(mountPath: string, owner: string): Promise<boolean> {
    const normalized = normalizeShareMountPath(mountPath, owner);
    return this.#dav.remove(normalized);
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

  async #ensureCollection(path: string): Promise<void> {
    try {
      await this.#dav.mkdir(path);
    } catch (error) {
      // WebDAV answers 405 when MKCOL targets an existing collection.
      if (!(error instanceof RelayError) || error.status !== 405) throw error;
    }
  }
}
