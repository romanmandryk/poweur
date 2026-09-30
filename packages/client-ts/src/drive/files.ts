/** Encrypted owner file workflows. Names and keys never leave this client. */
import { argon2id } from "@noble/hashes/argon2.js";
import { signBytes, x25519PublicKey, type SealedPayload } from "../crypto/index.js";
import { fromBase64, randomBytes, toBase64url, utf8 } from "../encoding.js";
import { DriveClient, type DriveListing, type DriveNode, type ListedNode } from "./client.js";
import { driveContext, encryptChunk, decryptChunk, sealKey, openKey, MAX_PLAINTEXT } from "./crypto.js";
import { canonicalManifest, publicNameHash, verifyManifest, verifyManifestPages, splitPages, type Manifest, type FileMode } from "./manifest.js";
import { nameHash, sealName, openName, normalizeName } from "./names.js";
import { appendRecordHash, canonicalAppendRecord, openRecordContent, sealRecordContent, verifyAppendRecord, verifyNextRecord, type AppendRecord, type ChunkRef, type DriveSealedPayload } from "./records.js";
import { canonicalShare, guestKey, keyBearing, roleGrants, SHARE_KDF, verifierHash, verifyShare, type Share, type ShareCaps, type ShareRole } from "./share.js";

const expired = (share: Share) => Boolean(share.expires) && Date.parse(share.expires!) <= Date.now();

const id = () => Array.from(randomBytes(16), b => b.toString(16).padStart(2, "0")).join("");
const wire = (p: SealedPayload): DriveSealedPayload => ({ ephemeral_public_key: p.ephemeralPublicKey, nonce: p.nonce, ciphertext: p.ciphertext });
const payload = (p: DriveSealedPayload): SealedPayload => ({ ephemeralPublicKey: p.ephemeral_public_key, nonce: p.nonce, ciphertext: p.ciphertext });

export interface FileKeys {
  /** Sign binary canonical bytes without converting them to UTF-8 text. */
  sign(bytes: Uint8Array): Promise<Uint8Array>;
  encryptionPrivateKey: Uint8Array;
  /** Resolves collaborators. The owner's own key is used without this. */
  authorKey?(author: string): Promise<Uint8Array>;
  /** Resolves a group identity's members and admins, to verify versions
   * written through a share to the group. Without it they fail closed. */
  groupMembers?(group: string): Promise<string[]>;
  /** Resolves a member's X25519 encryption key, to re-issue their share
   * after a key rotation. Without it rotation reports their share as stale. */
  encryptionKey?(member: string): Promise<Uint8Array>;
  /** Per group the caller belongs to, the group's epoch private keys newest
   * first (E24-T3, from `groups.keys()`): a share to that group is theirs. */
  groupKeys?: Record<string, Uint8Array[]>;
}
/** Adapter for CLI/local key custody. Browser callers can supply their own signer. */
export function fileKeys(signingPrivateKey: Uint8Array, encryptionPrivateKey: Uint8Array): FileKeys {
  return { sign: async bytes => signBytes(signingPrivateKey, bytes), encryptionPrivateKey };
}
export interface OpenFile {
  manifest: Manifest;
  /** Decrypted display name; the root has an empty name. */
  name: string;
  folder: string;
  nodeKey: Uint8Array;
  contentKey?: Uint8Array;
  /** Append files, as of when the node was opened or last listed: the last
   * record position and the first retained one. */
  position?: number;
  trimmedBefore?: number;
  /** Published: plaintext name and content key, served at /pub (E20-T5).
   * A public node has no node key (`nodeKey` is empty). */
  public?: boolean;
}
/** Random-access plaintext source used for bounded-memory uploads. Browser
 * `File` and Node file handles can implement this without loading the whole
 * object into memory. */
export interface DriveFileSource {
  size: number;
  slice(start: number, end: number): Promise<Uint8Array>;
}
export interface DriveUploadProgress { offset: number; total: number }
/** Per-author chain state while reading an append file, so a later read can
 * continue from where an earlier one stopped and still check every link. */
export type AuthorCursors = Map<string, { sequence: number; previous: string }>;
/** Records at most this large may be sealed inline in the record itself. */
export const MAX_INLINE_RECORD = 16 * 1024;

export class DriveFiles {
  private shares?: Share[];
  /** Per node: every share that ever stood on it or its ancestors (active,
   * expired or revoked). Evidence of past authority, never access. */
  private readonly nodeShares = new Map<string, Share[]>();
  /** Decrypted nodes by ID, reused while their head is unchanged. */
  private readonly opened = new Map<string, OpenFile>();
  constructor(readonly client: DriveClient, private readonly keys: FileKeys) {
    if (keys.encryptionPrivateKey.length !== 32) throw new Error("invalid encryption key");
  }
  private context(m: Manifest, purpose: string): Uint8Array { return driveContext(m.drive, m.node, purpose, m.generation); }
  private async signed(m: Manifest): Promise<Manifest> {
    m.signature = toBase64url(await this.keys.sign(canonicalManifest(m)));
    await this.verify(m);
    return m;
  }
  /** Whether the caller owns the drive (members and link holders do not). */
  private get owner(): boolean { return !this.client.link && this.client.signer.identity === this.client.drive; }
  private async authorKeyOf(author: string): Promise<Uint8Array> {
    const guest = guestKey(author);
    if (guest) return guest;
    const key = author === this.client.signer.identity
      ? fromBase64(this.client.signer.publicKey.replace(/^ed25519:/, ""))
      : await this.keys.authorKey?.(author);
    if (!key) throw new Error("untrusted manifest author");
    return key;
  }
  private async verify(m: Manifest): Promise<void> {
    if (m.drive !== this.client.drive) throw new Error("manifest drive mismatch");
    verifyManifest(m, await this.authorKeyOf(m.author));
    if (m.author !== this.client.drive) await this.allowAuthor(m);
  }
  private async sharesOn(node: string): Promise<Share[]> {
    let shares = this.nodeShares.get(node);
    if (!shares) {
      const listed = await this.client.nodeShares(node);
      shares = [...listed.shares, ...(listed.revoked ?? []).map(entry => entry.share)];
      this.nodeShares.set(node, shares);
    }
    return shares;
  }
  /** A share is trusted when its issuer signed it and could grant it: the
   * owner, or an admin through a share that is itself trusted. */
  private async trusted(share: Share, depth = 0): Promise<boolean> {
    if (share.drive !== this.client.drive || depth > 8) return false;
    try { verifyShare(share, await this.authorKeyOf(share.issuer)); } catch { return false; }
    if (share.issuer === this.client.drive) return true;
    for (const grant of await this.sharesOn(share.node)) {
      // An admin share that has since expired or been revoked still shows the
      // issuer could grant this share when it was made.
      if (grant.member === share.issuer && grant.role === "admin" && await this.trusted(grant, depth + 1)) return true;
    }
    return false;
  }
  /** Whether a share covers an author: its member, a member of the group it
   * names, or — for a guest author — the link it grants. */
  private async holds(share: Share, author: string): Promise<boolean> {
    if (guestKey(author)) return Boolean(share.link);
    if (share.member === author) return true;
    if (!share.member || !this.keys.groupMembers) return false;
    try { return (await this.keys.groupMembers(share.member)).some(m => m.toLowerCase() === author.toLowerCase()); } catch { return false; }
  }
  private async allowed(node: string, author: string, need: ShareRole): Promise<boolean> {
    if (author === this.client.drive) return true;
    for (const share of await this.sharesOn(node)) {
      // Expired and revoked shares still count: versions written while they
      // stood stay valid. Access itself is decided by the relay.
      if (roleGrants(share.role, need) && await this.holds(share, author) && await this.trusted(share)) return true;
    }
    return false;
  }
  private async allowAuthor(m: Manifest): Promise<void> {
    const need = m.operation === "create" ? "create" : m.operation === "rotate" ? "admin" : "write";
    const target = m.folder && (m.operation === "create" || m.operation === "move") ? m.folder : m.node;
    if (await this.allowed(target, m.author, need)) return;
    // The evidence may predate a share granted since it was read: once more, fresh.
    this.nodeShares.delete(target);
    if (!await this.allowed(target, m.author, need)) throw new Error("author role does not allow this version");
  }
  /** Whether a share is the caller's: by name or link, or to a group whose keys they hold. */
  private isMine(share: Share): boolean {
    const link = this.client.link?.id;
    if (link) return share.link === link;
    if (share.member === this.client.signer.identity) return true;
    return !!share.member && (this.keys.groupKeys?.[share.member.toLowerCase()]?.length ?? 0) > 0;
  }
  /** A share's node key: the caller's key opens their own share, a group's epoch keys a group share. */
  private openShareKey(share: Share, context: Uint8Array): Uint8Array {
    const groupKeys = share.member && share.member !== this.client.signer.identity ? this.keys.groupKeys?.[share.member.toLowerCase()] : undefined;
    if (groupKeys?.length) {
      for (const key of groupKeys) {
        try { return openKey(key, payload(share.node_key!), context); } catch { /* sealed at another epoch */ }
      }
      throw new Error("none of the group's keys opens this share; ask for it to be re-issued");
    }
    return openKey(this.keys.encryptionPrivateKey, payload(share.node_key!), context);
  }
  /** The caller's own key-bearing, trusted share on node. */
  private async myShare(node: string): Promise<Share | undefined> {
    this.shares ??= (await this.client.shares()).shares;
    for (const share of this.shares) {
      if (this.isMine(share) && share.node === node && share.node_key && !expired(share)) {
        if (!await this.trusted(share)) throw new Error("share is not signed by someone who may grant it");
        return share;
      }
    }
    return undefined;
  }
  /** Every node shared with the caller that carries a key. */
  async shared(): Promise<OpenFile[]> {
    this.shares ??= (await this.client.shares()).shares;
    const out: OpenFile[] = [], seen = new Set<string>();
    for (const share of this.shares) {
      if (!this.isMine(share) || !share.node_key || seen.has(share.node) || expired(share)) continue;
      seen.add(share.node);
      out.push(await this.open(share.node));
    }
    return out;
  }
  private async version(node: string, version: string): Promise<Manifest> {
    const m = await this.client.version(node, version);
    if (m.node !== node || m.version !== version) throw new Error("manifest reference mismatch");
    await this.verify(m);
    return m;
  }
  /** Open a node: one request returns it and its readable ancestors with the
   * versions carrying their envelopes; each is decrypted with its parent's
   * key (or the caller's share) from the top down, reusing nodes already
   * decrypted at the same head. Every version used is verified. */
  async open(node: string): Promise<OpenFile> {
    const { path } = await this.client.ancestry(node);
    if (!path.length || path[0]!.id !== node || path.length > 256) throw new Error("invalid folder ancestry");
    // A member starts at the highest node their share opens: the relay may
    // list ancestors they may read but hold no key for (a group admin sees
    // the group's whole drive, whose root is sealed to the group alone).
    let start = path.length - 1;
    if (!this.owner) {
      for (let i = path.length - 1; i > 0; i--) {
        if (await this.myShare(path[i]!.id)) break;
        start = i - 1;
      }
    }
    let parent: OpenFile | undefined;
    for (let i = start; i >= 0; i--) {
      const entry = path[i]!;
      if (i < path.length - 1 && entry.folder !== path[i + 1]!.id) throw new Error("invalid folder ancestry");
      parent = await this.fromListed(entry, parent);
    }
    return parent!;
  }
  /** Open a node from a listing entry, with its parent when the caller has it. */
  private async fromListed(entry: ListedNode, parent?: OpenFile): Promise<OpenFile> {
    const cached = this.opened.get(entry.id);
    if (cached && cached.manifest.version === entry.head && !entry.removed) {
      // Appends move the position without a new version. The relay omits
      // zeros, so an append file's missing fields mean 0.
      if (entry.mode === "append") { cached.position = entry.position ?? 0; cached.trimmedBefore = entry.trimmed_before ?? 0; }
      return cached;
    }
    if (entry.removed) throw new Error("node is removed");
    if (entry.public) return this.fromPublic(entry);
    const byVersion = new Map<string, Manifest>();
    for (const m of entry.versions ?? []) {
      if (m.node !== entry.id) throw new Error("manifest reference mismatch");
      byVersion.set(m.version, m);
    }
    const head = byVersion.get(entry.head);
    const pick = (version?: string) => version ? byVersion.get(version) : undefined;
    const keyVersion = pick(entry.key_version), nameVersion = pick(entry.name_version), contentVersion = pick(entry.content_version);
    // A relay that does not track envelope versions: walk the history.
    if (!head || !keyVersion || (entry.folder && !nameVersion) || (entry.kind === "file" && !contentVersion)) return this.openWalk(entry.id);
    if (head.generation !== entry.generation || head.kind !== entry.kind) throw new Error("node metadata mismatch");
    await Promise.all([...new Set([head, keyVersion, nameVersion, contentVersion].filter((m): m is Manifest => Boolean(m)))].map(m => this.verify(m)));
    return this.decrypt(entry, head, keyVersion, nameVersion, contentVersion,
      async () => parent && parent.manifest.node === entry.folder ? parent : entry.folder ? this.open(entry.folder) : undefined);
  }
  /** A public node: its name and content key are in its signed manifests. */
  private async fromPublic(entry: ListedNode): Promise<OpenFile> {
    const byVersion = new Map(entry.versions.map((m) => [m.version, m]));
    const head = byVersion.get(entry.head);
    const nameVersion = entry.name_version ? byVersion.get(entry.name_version) : undefined;
    const contentVersion = entry.content_version ? byVersion.get(entry.content_version) : undefined;
    if (!head || !head.public || head.node !== entry.id || !nameVersion?.plain_name || (entry.kind === "file" && !contentVersion?.plain_key)) throw new Error("public node metadata mismatch");
    for (const m of new Set([head, nameVersion, contentVersion].filter((v): v is Manifest => Boolean(v)))) {
      if (m.node !== entry.id || !m.public) throw new Error("manifest reference mismatch");
      await this.verify(m);
    }
    const result: OpenFile = { manifest: head, name: nameVersion.plain_name, folder: entry.folder ?? "", nodeKey: new Uint8Array(0), public: true,
      ...(contentVersion?.plain_key ? { contentKey: fromBase64(contentVersion.plain_key) } : {}),
      ...(entry.mode === "append" ? { position: entry.position ?? 0, trimmedBefore: entry.trimmed_before ?? 0 } : {}) };
    this.opened.set(entry.id, result);
    return result;
  }
  /** Reconstruct inherited envelopes across replacements and renames by
   * walking a node's history (relays without envelope tracking). Every
   * visited version is verified; cycles and excessive chains fail closed. */
  private async openWalk(node: string, ancestors = new Set<string>()): Promise<OpenFile> {
    if (ancestors.has(node) || ancestors.size >= 256) throw new Error("invalid folder ancestry");
    ancestors.add(node);
    const info = await this.client.node(node);
    if (info.id !== node || info.removed) throw new Error("node is removed or mismatched");
    const head = await this.version(node, info.head);
    if (head.generation !== info.generation || head.kind !== info.kind) throw new Error("node metadata mismatch");
    let keyVersion: Manifest | undefined, nameVersion: Manifest | undefined, contentVersion: Manifest | undefined;
    let m = head;
    const seen = new Set<string>();
    for (;;) {
      if (seen.has(m.version) || seen.size >= 10000) throw new Error("invalid version ancestry");
      seen.add(m.version);
      if (!keyVersion && m.node_key) keyVersion = m;
      if (!nameVersion && m.name) nameVersion = m;
      if (!contentVersion && m.content_key) contentVersion = m;
      if (keyVersion && (head.kind === "folder" || contentVersion) && (!info.folder || nameVersion)) break;
      if (!m.parent) throw new Error("missing key or name envelope");
      m = await this.version(node, m.parent);
    }
    return this.decrypt(info, head, keyVersion, nameVersion, contentVersion,
      async () => info.folder ? this.openWalk(info.folder, ancestors) : undefined);
  }
  /** Decrypt a node's key, name and content key from verified versions. */
  private async decrypt(info: DriveNode, head: Manifest, keyVersion: Manifest, nameVersion: Manifest | undefined, contentVersion: Manifest | undefined,
    parentOf: () => Promise<OpenFile | undefined>): Promise<OpenFile> {
    const node = info.id;
    if (keyVersion.generation !== head.generation || (contentVersion && contentVersion.generation !== head.generation)) throw new Error("key generation mismatch");
    // A member (or link holder) opens a shared node with its share's key and
    // what lies below through parents; only the owner walks up to the root.
    let nodeKey: Uint8Array | undefined, parentKey: Uint8Array | undefined;
    const share = this.owner ? undefined : await this.myShare(node);
    if (share) {
      if (share.generation !== head.generation) throw new Error("the share predates a key rotation; ask for it to be re-issued");
      nodeKey = this.openShareKey(share, driveContext(this.client.drive, node, "node-key", share.generation));
      if (toBase64url(x25519PublicKey(nodeKey)) !== share.node_public) throw new Error("share key does not match its node");
    } else {
      if (!info.folder && !this.owner) throw new Error("no share opens this node");
      const parent = info.folder ? await parentOf() : undefined;
      if (info.folder && parent?.manifest.node !== info.folder) throw new Error("parent mismatch");
      if (parent && parent.manifest.kind !== "folder") throw new Error("parent is not a folder");
      parentKey = parent?.nodeKey ?? this.keys.encryptionPrivateKey;
      nodeKey = openKey(parentKey, payload(keyVersion.node_key!), this.context(keyVersion, "node-key"));
    }
    // A name is sealed to its parent: a shared node's own name is known only
    // to those who can open the parent.
    const name = nameVersion && parentKey ? openName(parentKey, payload(nameVersion.name!), this.context(nameVersion, "name")) : "";
    // Create-only guests know the folder public key, but cannot compute its
    // private keyed name index. Their random token is replaced on rename.
    if (nameVersion && parentKey && !guestKey(nameVersion.author) && nameHash(parentKey, name) !== nameVersion.name_hash) throw new Error("name index mismatch");
    const result: OpenFile = { manifest: head, name, folder: info.folder ?? "", nodeKey,
      ...(info.mode === "append" ? { position: info.position ?? 0, trimmedBefore: info.trimmed_before ?? 0 } : {}) };
    if (contentVersion) result.contentKey = openKey(nodeKey, payload(contentVersion.content_key!), this.context(contentVersion, "content-key"));
    this.opened.set(node, result);
    return result;
  }
  async root(): Promise<OpenFile> {
    const { root } = await this.client.info();
    if (root) return this.open(root);
    return this.create(undefined, "", "folder");
  }
  /** A folder's children in one request per page, decrypted in parallel with
   * the folder's key; the page's share evidence refreshes author checks. */
  async list(folder: OpenFile): Promise<OpenFile[]> {
    if (folder.manifest.kind !== "folder") throw new Error("not a folder");
    const result: OpenFile[] = [];
    let cursor = "";
    const seen = new Set<string>();
    do {
      if (seen.has(cursor)) throw new Error("repeated children cursor");
      seen.add(cursor);
      const page = await this.client.listing(folder.manifest.node, cursor);
      if (page.folder.id !== folder.manifest.node) throw new Error("listing is for another folder");
      this.absorb(page);
      const children = await Promise.all(page.children.map(child => {
        if (child.folder !== folder.manifest.node) throw new Error("listed child is not in this folder");
        return this.fromListed(child, folder);
      }));
      result.push(...children);
      cursor = page.cursor;
    } while (cursor);
    return result;
  }
  /** Share evidence from a listing: shares on the folder's chain apply to the
   * folder and every child; a child's own shares only to it. */
  private absorb(page: DriveListing): void {
    const all = [...page.shares, ...(page.revoked ?? []).map(entry => entry.share)];
    const children = new Set(page.children.map(child => child.id));
    const chain = all.filter(share => !children.has(share.node));
    this.nodeShares.set(page.folder.id, chain);
    for (const child of page.children) this.nodeShares.set(child.id, [...chain, ...all.filter(share => share.node === child.id)]);
  }
  /** Absolute or root-relative paths; refuse empty interior segments and traversal. */
  async resolve(path: string): Promise<OpenFile> {
    const parts = path === "/" || path === "" ? [] : path.replace(/^\//, "").split("/").map(normalizeName);
    // The owner's paths start at the root; a member's at a node shared with
    // them, named by its ID: /<node-id>/sub/path.
    let current: OpenFile;
    if (this.owner) current = await this.root();
    else if (!parts.length) throw new Error("a shared path starts with the shared node's ID");
    else current = await this.open(parts.shift()!);
    for (const name of parts) {
      const next = (await this.list(current)).find(child => child.name === name);
      if (!next) throw new Error(`drive path not found: ${name}`);
      current = next;
    }
    return current;
  }
  async create(parent: OpenFile | undefined, name: string, kind: "file" | "folder", bytes: Uint8Array<ArrayBufferLike> = new Uint8Array(), mode: FileMode = "replace"): Promise<OpenFile> {
    if (parent && parent.manifest.kind !== "folder") throw new Error("not a folder");
    if (!parent && kind !== "folder") throw new Error("root must be a folder");
    // Everything inside a public folder is public.
    if (parent?.public) return this.createPublic(parent, name, kind, bytes, mode);
    const nodeKey = randomBytes(32), contentKey = kind === "file" ? randomBytes(32) : undefined;
    const m: Manifest = { format: 1, drive: this.client.drive, node: id(), version: id(), parent: "", operation: "create",
      author: this.client.signer.identity, generation: 1, kind, mode: kind === "file" ? mode : "", folder: parent?.manifest.node ?? "",
      name_hash: "", count: 0, pages: [], signature: "" };
    const parentKey = parent?.nodeKey ?? this.keys.encryptionPrivateKey;
    m.node_key = wire(sealKey(x25519PublicKey(parentKey), nodeKey, this.context(m, "node-key")));
    if (parent) {
      name = normalizeName(name);
      m.name = wire(sealName(x25519PublicKey(parentKey), name, this.context(m, "name")));
      m.name_hash = nameHash(parentKey, name);
    }
    if (contentKey) m.content_key = wire(sealKey(x25519PublicKey(nodeKey), contentKey, this.context(m, "content-key")));
    if ((kind === "folder" || mode === "append") && bytes.length) throw new Error("initial bytes require a replace file");
    const pages = contentKey ? await this.upload(m, contentKey, bytes) : [];
    await this.client.commit({ manifest: await this.signed(m), pages });
    return { manifest: m, name, folder: parent?.manifest.node ?? "", nodeKey, ...(contentKey ? { contentKey } : {}) };
  }
  /**
   * Publish: create a public node (E20-T5). Its name and content key go into
   * the signed manifest in the clear, so anyone can read it at
   * https://<identity>/pub/…. A public tree starts directly under the root.
   */
  async createPublic(parent: OpenFile, name: string, kind: "file" | "folder", bytes: Uint8Array<ArrayBufferLike> = new Uint8Array(), mode: FileMode = "replace"): Promise<OpenFile> {
    if (parent.manifest.kind !== "folder") throw new Error("not a folder");
    if (!parent.public && parent.folder !== "") throw new Error("a public folder is created at the top of the drive");
    name = normalizeName(name);
    const contentKey = kind === "file" ? randomBytes(32) : undefined;
    const m: Manifest = { format: 1, drive: this.client.drive, node: id(), version: id(), parent: "", operation: "create",
      author: this.client.signer.identity, generation: 1, kind, mode: kind === "file" ? mode : "", folder: parent.manifest.node,
      name_hash: publicNameHash(parent.manifest.node, name), count: 0, pages: [], public: true, plain_name: name,
      ...(contentKey ? { plain_key: toBase64url(contentKey) } : {}), signature: "" };
    if ((kind === "folder" || mode === "append") && bytes.length) throw new Error("initial bytes require a replace file");
    const pages = contentKey ? await this.upload(m, contentKey, bytes) : [];
    await this.client.commit({ manifest: await this.signed(m), pages });
    const created: OpenFile = { manifest: m, name, folder: parent.manifest.node, nodeKey: new Uint8Array(0), public: true, ...(contentKey ? { contentKey } : {}) };
    this.opened.set(m.node, created);
    return created;
  }
  /** Create a replace file from a random-access source one encrypted chunk at
   * a time. This is the upload path for multi-gigabyte browser transfers: its
   * memory use is bounded by one drive chunk, and each chunk upload retains
   * the client's normal retry/content-address checks. */
  async createFromSource(parent: OpenFile, name: string, source: DriveFileSource,
    onProgress?: (progress: DriveUploadProgress) => void | Promise<void>): Promise<OpenFile> {
    if (parent.manifest.kind !== "folder") throw new Error("not a folder");
    if (!Number.isSafeInteger(source.size) || source.size < 0) throw new Error("invalid source size");
    name = normalizeName(name);
    const nodeKey = randomBytes(32), contentKey = randomBytes(32);
    const m: Manifest = { format: 1, drive: this.client.drive, node: id(), version: id(), parent: "", operation: "create",
      author: this.client.signer.identity, generation: 1, kind: "file", mode: "replace", folder: parent.manifest.node,
      name_hash: "", count: 0, pages: [], signature: "" };
    m.node_key = wire(sealKey(x25519PublicKey(parent.nodeKey), nodeKey, this.context(m, "node-key")));
    m.name = wire(sealName(x25519PublicKey(parent.nodeKey), name, this.context(m, "name")));
    m.name_hash = nameHash(parent.nodeKey, name);
    m.content_key = wire(sealKey(x25519PublicKey(nodeKey), contentKey, this.context(m, "content-key")));
    const refs: ChunkRef[] = [];
    for (let offset = 0; offset < source.size; offset += MAX_PLAINTEXT) {
      const end = Math.min(source.size, offset + MAX_PLAINTEXT);
      const plain = await source.slice(offset, end);
      if (plain.length !== end - offset) throw new Error("source returned the wrong byte range");
      const encrypted = encryptChunk(contentKey, plain, this.context(m, "content"));
      refs.push(...await this.client.store([encrypted]));
      await onProgress?.({ offset: end, total: source.size });
    }
    const { pages, hashes } = splitPages(m.drive, m.node, refs);
    m.pages = hashes; m.count = refs.length;
    await this.client.commit({ manifest: await this.signed(m), pages });
    return { manifest: m, name, folder: parent.manifest.node, nodeKey, contentKey };
  }
  private async upload(m: Manifest, key: Uint8Array, bytes: Uint8Array) {
    const blobs: Uint8Array[] = [];
    for (let offset = 0; offset < bytes.length; offset += MAX_PLAINTEXT) {
      blobs.push(encryptChunk(key, bytes.subarray(offset, offset + MAX_PLAINTEXT), this.context(m, "content")));
    }
    const refs = await this.client.store(blobs);
    const { pages, hashes } = splitPages(m.drive, m.node, refs);
    m.pages = hashes; m.count = refs.length;
    return pages;
  }
  private next(file: OpenFile, operation: Manifest["operation"]): Manifest {
    return { ...file.manifest, version: id(), parent: file.manifest.version, operation, author: this.client.signer.identity,
      folder: "", name: undefined, name_hash: "", node_key: undefined, content_key: undefined,
      plain_name: undefined, plain_key: undefined, signature: "" };
  }
  async replace(file: OpenFile, bytes: Uint8Array): Promise<void> {
    if (!file.contentKey || file.manifest.mode !== "replace") throw new Error("not a replace file");
    const m = this.next(file, "replace"), pages = await this.upload(m, file.contentKey, bytes);
    await this.client.commit({ manifest: await this.signed(m), pages });
    file.manifest = m;
  }
  /** Bounded-memory plaintext stream; each chunk is authenticated before yield. */
  async *read(file: OpenFile): AsyncGenerator<Uint8Array> {
    if (!file.contentKey || file.manifest.mode !== "replace") throw new Error("not a replace file");
    await this.verify(file.manifest);
    const pages = [];
    for (const hash of file.manifest.pages) pages.push(await this.client.page(file.manifest.node, file.manifest.version, hash));
    const refs = verifyManifestPages(file.manifest, pages);
    for (const ref of refs) {
      const bytes = await this.client.chunk(file.manifest.node, ref, file.manifest.version);
      yield decryptChunk(file.contentKey, bytes, this.context(file.manifest, "content"));
    }
  }
  /**
   * Re-key a node and everything below it, as the relay requires after a
   * revocation (`rotate_required`): new node keys, a new content key and
   * re-encrypted content for replace files, names and keys re-sealed under
   * each re-keyed folder, and the remaining members' shares re-issued at the
   * new generation (when `keys.encryptionKey` can resolve them). Append files
   * keep their node and content keys — past records, including guests'
   * sealed ones, cannot be re-encrypted — so new records there are protected
   * by access control, not re-keying. Links on a
   * rotated node carry the retired key and must be recreated. Needs the
   * node's parent key: the drive's owner, or an admin of the parent.
   */
  async rotate(file: OpenFile): Promise<{ file: OpenFile; reissued: number; stale: Share[] }> {
    const parentKey = file.folder ? (await this.open(file.folder)).nodeKey : this.keys.encryptionPrivateKey;
    if (!file.folder && !this.owner) throw new Error("only the owner rotates the root");
    // Read the whole subtree with the old keys first: once a folder is
    // re-keyed, its children no longer open through the relay's tree.
    type Subtree = { node: OpenFile; bytes?: Uint8Array; children: Subtree[] };
    const collect = async (node: OpenFile): Promise<Subtree> => ({
      node,
      ...(node.manifest.kind === "file" && node.manifest.mode === "replace" ? { bytes: await readAll(this.read(node)) } : {}),
      children: node.manifest.kind === "folder" ? await Promise.all((await this.list(node)).map(collect)) : [],
    });
    const tree = await collect(file);
    const shares = (await this.client.shares()).shares;
    let reissued = 0;
    const stale: Share[] = [];
    const rekey = async (entry: Subtree, parentKey: Uint8Array, parent?: OpenFile): Promise<OpenFile> => {
      const old = entry.node;
      const m = this.next(old, "rotate");
      m.generation = old.manifest.generation + 1;
      const append = old.manifest.kind === "file" && old.manifest.mode === "append";
      const nodeKey = append ? old.nodeKey : randomBytes(32);
      m.node_key = wire(sealKey(x25519PublicKey(parentKey), nodeKey, this.context(m, "node-key")));
      let contentKey = old.contentKey, pages: Awaited<ReturnType<DriveFiles["upload"]>> = [];
      if (old.manifest.kind === "file") {
        if (old.manifest.mode === "replace") {
          contentKey = randomBytes(32);
          m.content_key = wire(sealKey(x25519PublicKey(nodeKey), contentKey, this.context(m, "content-key")));
          pages = await this.upload(m, contentKey, entry.bytes!);
        } else {
          m.content_key = wire(sealKey(x25519PublicKey(nodeKey), contentKey!, this.context(m, "content-key")));
        }
      }
      await this.client.commit({ manifest: await this.signed(m), pages });
      const rotated: OpenFile = { ...old, manifest: m, nodeKey, ...(contentKey ? { contentKey } : {}) };
      // The name was sealed to the parent's retired key: re-seal it in place.
      if (parent) { await this.move(rotated, parent, rotated.name); }
      for (const share of shares.filter(s => s.node === old.manifest.node && keyBearing(s.role) && s.member && s.generation < m.generation)) {
        const recipient = this.keys.encryptionKey ? await this.keys.encryptionKey(share.member!).catch(() => undefined) : undefined;
        if (!recipient) { stale.push(share); continue; }
        await this.grant(rotated, share.member!, "", share.role, share.expires ?? "", recipient, undefined, undefined, share.caps, share.pow ?? 0);
        await this.client.unshare(share.id);
        reissued++;
      }
      for (const link of shares.filter(s => s.node === old.manifest.node && keyBearing(s.role) && s.link)) stale.push(link);
      for (const child of entry.children) await rekey(child, nodeKey, rotated);
      return rotated;
    };
    const rotated = await rekey(tree, parentKey);
    this.shares = undefined;
    this.nodeShares.clear();
    return { file: rotated, reissued, stale };
  }
  /** Rotates node when a revocation left it waiting for new keys. */
  async rotateIfRequired(node: string): Promise<Awaited<ReturnType<DriveFiles["rotate"]>> | undefined> {
    const info = await this.client.node(node);
    if (!info.rotate_required || info.removed) return undefined;
    return this.rotate(await this.open(node));
  }
  async move(file: OpenFile, parent: OpenFile, name: string): Promise<void> {
    if (parent.manifest.kind !== "folder") throw new Error("not a folder");
    name = normalizeName(name);
    if (Boolean(file.public) !== Boolean(parent.public) && !(file.public && parent.folder === "")) {
      throw new Error(file.public ? "a public item moves only within public folders or to the top" : "publishing needs a copy: a private item cannot move into a public folder");
    }
    const m = this.next(file, "move");
    m.folder = parent.manifest.node;
    if (file.public) {
      m.plain_name = name;
      m.name_hash = publicNameHash(parent.manifest.node, name);
      await this.client.commit({ manifest: await this.signed(m) });
      file.manifest = m; file.name = name; file.folder = parent.manifest.node;
      return;
    }
    m.name = wire(sealName(x25519PublicKey(parent.nodeKey), name, this.context(m, "name")));
    m.name_hash = nameHash(parent.nodeKey, name);
    m.node_key = wire(sealKey(x25519PublicKey(parent.nodeKey), file.nodeKey, this.context(m, "node-key")));
    await this.client.commit({ manifest: await this.signed(m) });
    file.manifest = m; file.name = name; file.folder = parent.manifest.node;
  }
  async remove(file: OpenFile): Promise<void> {
    const m = this.next(file, "remove"); m.count = 0; m.pages = [];
    await this.client.commit({ manifest: await this.signed(m) });
    file.manifest = m;
  }
  /** `length` below zero reads through the end. Earlier fixed-size chunks are not downloaded. */
  async *readRange(file: OpenFile, offset: number, length: number): AsyncGenerator<Uint8Array> {
    if (offset < 0 || length === 0) { if (length === 0 && offset >= 0) return; throw new Error("invalid range"); }
    if (!file.contentKey || file.manifest.mode !== "replace") throw new Error("not a replace file");
    await this.verify(file.manifest);
    const pages = [];
    for (const hash of file.manifest.pages) pages.push(await this.client.page(file.manifest.node, file.manifest.version, hash));
    const refs = verifyManifestPages(file.manifest, pages);
    if (!refs.length || Math.floor(offset / MAX_PLAINTEXT) >= refs.length) return;
    const start = Math.floor(offset / MAX_PLAINTEXT);
    let end = refs.length;
    if (length > 0) end = Math.min(refs.length, Math.ceil((offset + length) / MAX_PLAINTEXT));
    for (let i = start; i < end; i++) {
      const plain = decryptChunk(file.contentKey, await this.client.chunk(file.manifest.node, refs[i]!, file.manifest.version), this.context(file.manifest, "content"));
      if (i !== refs.length - 1 && plain.length !== MAX_PLAINTEXT) throw new Error("variable chunk size; read from the start");
      const chunkStart = i * MAX_PLAINTEXT;
      let from = offset > chunkStart ? offset - chunkStart : 0;
      let to = plain.length;
      if (length > 0 && chunkStart + to > offset + length) to = offset + length - chunkStart;
      if (from < to) yield plain.subarray(from, to);
    }
  }
  /** Records from `from`, at most `limit` of them (0: to the end). */
  private async recordsFrom(node: string, from: number, limit = 0): Promise<{ position: number; record: AppendRecord }[]> {
    const out: { position: number; record: AppendRecord }[] = [];
    const seen = new Set<number>();
    for (;;) {
      if (seen.has(from)) throw new Error("repeated record cursor");
      seen.add(from);
      const want = limit ? Math.min(1000, limit - out.length) : 1000;
      const page = await this.client.records(node, from, want);
      out.push(...page.records);
      // A short page is the last one: no empty request to confirm it.
      if (page.records.length < want || page.next === from || (limit && out.length >= limit)) return out;
      from = page.next;
    }
  }
  /** The caller's own chain position in an append file, from the relay: one
   * request instead of reading the whole log. The relay enforces the chain on
   * commit, so a wrong answer only makes the append fail. */
  async authorCursor(file: OpenFile): Promise<{ sequence: number; previous: string }> {
    const cursor = await this.client.authorCursor(file.manifest.node);
    if (!Number.isSafeInteger(cursor.sequence) || cursor.sequence < 0 || (cursor.sequence === 0) !== (cursor.previous === "")) throw new Error("invalid author cursor");
    return cursor;
  }
  async append(file: OpenFile, plaintext: Uint8Array, options: { inline?: boolean; cursor?: { sequence: number; previous: string } } = {}): Promise<number> {
    if (file.manifest.mode !== "append" || !file.contentKey) throw new Error("not an append file");
    if (!plaintext.length) throw new Error("empty append");
    const cursor = options.cursor ?? await this.authorCursor(file);
    let record: AppendRecord = { format: 1, drive: this.client.drive, node: file.manifest.node, author: this.client.signer.identity,
      generation: file.manifest.generation, sequence: cursor.sequence + 1, previous: cursor.previous, chunks: [], signature: "" };
    if (options.inline && plaintext.length <= MAX_INLINE_RECORD) {
      record = sealRecordContent(record, x25519PublicKey(file.nodeKey), plaintext);
    } else {
      const blobs: Uint8Array[] = [];
      for (let offset = 0; offset < plaintext.length; offset += MAX_PLAINTEXT) {
        blobs.push(encryptChunk(file.contentKey, plaintext.subarray(offset, offset + MAX_PLAINTEXT), this.context(file.manifest, "content")));
      }
      record.chunks = await this.client.store(blobs);
    }
    record.signature = toBase64url(await this.keys.sign(canonicalAppendRecord(record)));
    const result = await this.client.commit({ records: [record] });
    const position = result.positions?.[0];
    if (position == null) throw new Error("append returned no position");
    if (options.cursor) { options.cursor.sequence = record.sequence; options.cursor.previous = appendRecordHash(record); }
    file.position = position;
    return position;
  }
  /** Records from `from` on, each checked against its author's chain. Pass
   * the cursors an earlier read filled in to continue those chains. */
  async tail(file: OpenFile, from = 0, cursors: AuthorCursors = new Map(), limit = 0): Promise<{ position: number; author: string; sequence: number; plain: Uint8Array }[]> {
    if (file.manifest.mode !== "append" || !file.contentKey) throw new Error("not an append file");
    const trimmedBefore = file.trimmedBefore ?? (await this.client.node(file.manifest.node)).trimmed_before ?? 0;
    if (!from) from = 1;
    if (trimmedBefore > from) throw new Error("record prefix was trimmed; load the snapshot");
    const out = [];
    for (const item of await this.recordsFrom(file.manifest.node, from, limit)) {
      const key = await this.authorKeyOf(item.record.author);
      if (!await this.allowed(file.manifest.node, item.record.author, "append")) throw new Error("record author may not append to this file");
      const cur = cursors.get(item.record.author) ?? { sequence: 0, previous: "" };
      if (cur.sequence === 0 && item.record.sequence !== 1) verifyAppendRecord(item.record, key);
      else verifyNextRecord(item.record, key, cur.sequence, cur.previous);
      cursors.set(item.record.author, { sequence: item.record.sequence, previous: appendRecordHash(item.record) });
      const plain = item.record.sealed ? openRecordContent(item.record, file.nodeKey) : await this.recordChunks(file, item.record);
      out.push({ position: item.position, author: item.record.author, sequence: item.record.sequence, plain });
    }
    return out;
  }
  private async recordChunks(file: OpenFile, record: AppendRecord): Promise<Uint8Array> {
    const parts: Uint8Array[] = [];
    // A record is encrypted under the key generation it was written at; an
    // append file keeps its content key across rotations.
    const context = driveContext(file.manifest.drive, file.manifest.node, "content", record.generation);
    for (const ref of record.chunks) parts.push(decryptChunk(file.contentKey!, await this.client.chunk(file.manifest.node, ref), context));
    const size = parts.reduce((sum, part) => sum + part.length, 0);
    const out = new Uint8Array(size);
    let offset = 0;
    for (const part of parts) { out.set(part, offset); offset += part.length; }
    return out;
  }
  async shareWith(file: OpenFile, member: string, memberPublic: Uint8Array, role: ShareRole, expires = ""): Promise<Share> {
    return this.grant(file, member, "", role, expires, memberPublic);
  }
  async link(file: OpenFile, role: ShareRole, expires = "", password = "", options: { caps?: ShareCaps; pow?: number } = {}): Promise<{ share: Share; fragment: Uint8Array }> {
    const link = id();
    const fragment = randomBytes(32);
    let secret = fragment, salt: Uint8Array | undefined, verifier: Uint8Array | undefined;
    if (password) {
      salt = randomBytes(16);
      const derived = argon2id(utf8(password), salt, { t: 3, m: 65536, p: 1, dkLen: 64 });
      secret = linkSecret(fragment, derived.subarray(0, 32));
      verifier = derived.subarray(32);
    }
    const share = await this.grant(file, "", link, role, expires, x25519PublicKey(secret), salt, verifier, options.caps, options.pow);
    return { share, fragment };
  }
  private async grant(file: OpenFile, member: string, link: string, role: ShareRole, expires: string, recipient: Uint8Array, salt?: Uint8Array, verifier?: Uint8Array, caps: ShareCaps = {}, pow = 0): Promise<Share> {
    const share: Share = { format: 1, drive: this.client.drive, id: id(), node: file.manifest.node, ...(member ? { member } : { link }), role,
      generation: file.manifest.generation, node_public: toBase64url(x25519PublicKey(file.nodeKey)), ...(expires ? { expires } : {}), caps, ...(pow ? { pow } : {}),
      issuer: this.client.signer.identity, issued: new Date().toISOString().replace(/\.\d{3}Z$/, "Z"), signature: "" };
    if (keyBearing(role)) {
      if (recipient.length !== 32) throw new Error("share recipient key must be 32 bytes");
      share.node_key = wire(sealKey(recipient, file.nodeKey, this.context(file.manifest, "node-key")));
    }
    if (salt && verifier) { share.kdf = SHARE_KDF; share.salt = toBase64url(salt); share.verifier_hash = verifierHash(verifier); }
    share.signature = toBase64url(await this.keys.sign(canonicalShare(share)));
    const signed = share;
    await this.client.commit({ share: signed });
    this.shares = undefined;
    return signed;
  }
  async transfer(node: OpenFile, dst: DriveFiles, parent: OpenFile): Promise<OpenFile> {
    if (this.client.drive === dst.client.drive) throw new Error("a transfer names another drive");
    if (!node.folder) throw new Error("the root cannot be transferred");
    const copied = await this.copyNode(dst, parent, node, new Set());
    await this.client.commit({ transfer: { node: node.manifest.node, to: dst.client.drive, to_node: copied.manifest.node } });
    return copied;
  }
  private async copyNode(dst: DriveFiles, parent: OpenFile, node: OpenFile, seen: Set<string>): Promise<OpenFile> {
    if (seen.has(node.manifest.node) || seen.size >= 10000) throw new Error("invalid copy");
    seen.add(node.manifest.node);
    if (node.manifest.kind === "folder") {
      const created = await dst.create(parent, node.name, "folder");
      for (const child of await this.list(node)) await this.copyNode(dst, created, child, seen);
      return created;
    }
    if (node.manifest.mode === "append") {
      const created = await dst.create(parent, node.name, "file", new Uint8Array(), "append");
      for (const record of await this.tail(node, 1)) await dst.append(created, record.plain);
      return created;
    }
    return dst.create(parent, node.name, "file", await readAll(this.read(node)));
  }
}

/** URL fragment mixed with the first half of a password KDF. The fragment alone is the key when there is no password. */
export function linkSecret(fragment: Uint8Array, passwordHalf?: Uint8Array): Uint8Array {
  if (fragment.length !== 32 || (passwordHalf && passwordHalf.length !== 32)) throw new Error("link secret must be 32 bytes");
  const out = new Uint8Array(fragment);
  if (passwordHalf) for (let i = 0; i < 32; i++) out[i] = (out[i] ?? 0) ^ (passwordHalf[i] ?? 0);
  return out;
}
async function readAll(chunks: AsyncGenerator<Uint8Array>): Promise<Uint8Array> {
  const parts: Uint8Array[] = [];
  for await (const chunk of chunks) parts.push(chunk);
  const size = parts.reduce((sum, part) => sum + part.length, 0);
  const out = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) { out.set(part, offset); offset += part.length; }
  return out;
}
