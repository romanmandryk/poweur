/** Encrypted owner file workflows. Names and keys never leave this client. */
import { argon2id } from "@noble/hashes/argon2.js";
import { signBytes, x25519PublicKey, type SealedPayload } from "../crypto/index.js";
import { fromBase64, randomBytes, toBase64url, utf8 } from "../encoding.js";
import { DriveClient } from "./client.js";
import { driveContext, encryptChunk, decryptChunk, sealKey, openKey, MAX_PLAINTEXT } from "./crypto.js";
import { canonicalManifest, verifyManifest, verifyManifestPages, splitPages, type Manifest, type FileMode } from "./manifest.js";
import { nameHash, sealName, openName, normalizeName } from "./names.js";
import { appendRecordHash, canonicalAppendRecord, openRecordContent, verifyAppendRecord, verifyNextRecord, type AppendRecord, type ChunkRef, type DriveSealedPayload } from "./records.js";
import { canonicalShare, guestKey, keyBearing, roleGrants, SHARE_KDF, verifierHash, verifyShare, type Share, type ShareRole } from "./share.js";

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
}

export class DriveFiles {
  private shares?: Share[];
  private readonly nodeShares = new Map<string, Share[]>();
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
    if (!shares) { shares = (await this.client.nodeShares(node)).shares; this.nodeShares.set(node, shares); }
    return shares;
  }
  /** A share is trusted when its issuer signed it and could grant it: the
   * owner, or an admin through a share that is itself trusted. */
  private async trusted(share: Share, depth = 0): Promise<boolean> {
    if (share.drive !== this.client.drive || depth > 8) return false;
    try { verifyShare(share, await this.authorKeyOf(share.issuer)); } catch { return false; }
    if (share.issuer === this.client.drive) return true;
    for (const grant of await this.sharesOn(share.node)) {
      if (grant.member === share.issuer && grant.role === "admin" && !expired(grant) && await this.trusted(grant, depth + 1)) return true;
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
      if (!expired(share) && roleGrants(share.role, need) && await this.holds(share, author) && await this.trusted(share)) return true;
    }
    return false;
  }
  private async allowAuthor(m: Manifest): Promise<void> {
    const need = m.operation === "create" ? "create" : m.operation === "rotate" ? "admin" : "write";
    const target = m.folder && (m.operation === "create" || m.operation === "move") ? m.folder : m.node;
    if (!await this.allowed(target, m.author, need)) throw new Error("author role does not allow this version");
  }
  /** The caller's own key-bearing, trusted share on node. */
  private async myShare(node: string): Promise<Share | undefined> {
    this.shares ??= (await this.client.shares()).shares;
    const link = this.client.link?.id;
    for (const share of this.shares) {
      const mine = link ? share.link === link : share.member === this.client.signer.identity;
      if (mine && share.node === node && share.node_key && !expired(share)) {
        if (!await this.trusted(share)) throw new Error("share is not signed by someone who may grant it");
        return share;
      }
    }
    return undefined;
  }
  /** Every node shared with the caller that carries a key. */
  async shared(): Promise<OpenFile[]> {
    this.shares ??= (await this.client.shares()).shares;
    const link = this.client.link?.id, out: OpenFile[] = [], seen = new Set<string>();
    for (const share of this.shares) {
      const mine = link ? share.link === link : share.member === this.client.signer.identity;
      if (!mine || !share.node_key || seen.has(share.node) || expired(share)) continue;
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
  /** Reconstruct inherited envelopes across replacements and renames. Every
   * visited version is verified; cycles and excessive chains fail closed. */
  async open(node: string, ancestors = new Set<string>()): Promise<OpenFile> {
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
    if (keyVersion.generation !== head.generation || (contentVersion && contentVersion.generation !== head.generation)) throw new Error("key generation mismatch");
    // A member (or link holder) opens a shared node with its share's key and
    // what lies below through parents; only the owner walks up to the root.
    let nodeKey: Uint8Array | undefined, parentKey: Uint8Array | undefined;
    const share = this.owner ? undefined : await this.myShare(node);
    if (share) {
      if (share.generation !== head.generation) throw new Error("the share predates a key rotation; ask for it to be re-issued");
      nodeKey = openKey(this.keys.encryptionPrivateKey, payload(share.node_key!), driveContext(this.client.drive, node, "node-key", share.generation));
      if (toBase64url(x25519PublicKey(nodeKey)) !== share.node_public) throw new Error("share key does not match its node");
    } else {
      if (!info.folder && !this.owner) throw new Error("no share opens this node");
      const parent = info.folder ? await this.open(info.folder, ancestors) : undefined;
      if (parent && parent.manifest.kind !== "folder") throw new Error("parent is not a folder");
      parentKey = parent?.nodeKey ?? this.keys.encryptionPrivateKey;
      nodeKey = openKey(parentKey, payload(keyVersion.node_key!), this.context(keyVersion, "node-key"));
    }
    // A name is sealed to its parent: a shared node's own name is known only
    // to those who can open the parent.
    const name = nameVersion && parentKey ? openName(parentKey, payload(nameVersion.name!), this.context(nameVersion, "name")) : "";
    if (nameVersion && parentKey && nameHash(parentKey, name) !== nameVersion.name_hash) throw new Error("name index mismatch");
    const result: OpenFile = { manifest: head, name, folder: info.folder ?? "", nodeKey };
    if (contentVersion) result.contentKey = openKey(nodeKey, payload(contentVersion.content_key!), this.context(contentVersion, "content-key"));
    return result;
  }
  async root(): Promise<OpenFile> {
    const { root } = await this.client.info();
    if (root) return this.open(root);
    return this.create(undefined, "", "folder");
  }
  async list(folder: OpenFile): Promise<OpenFile[]> {
    if (folder.manifest.kind !== "folder") throw new Error("not a folder");
    const result: OpenFile[] = [];
    let cursor = "";
    const seen = new Set<string>();
    do {
      if (seen.has(cursor)) throw new Error("repeated children cursor");
      seen.add(cursor);
      const page = await this.client.children(folder.manifest.node, cursor);
      for (const child of page.children) result.push(await this.open(child.id));
      cursor = page.cursor;
    } while (cursor);
    return result;
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
      folder: "", name: undefined, name_hash: "", node_key: undefined, content_key: undefined, signature: "" };
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
  async move(file: OpenFile, parent: OpenFile, name: string): Promise<void> {
    if (parent.manifest.kind !== "folder") throw new Error("not a folder");
    name = normalizeName(name);
    const m = this.next(file, "move");
    m.folder = parent.manifest.node;
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
  private async recordsFrom(node: string, from: number): Promise<{ position: number; record: AppendRecord }[]> {
    const out: { position: number; record: AppendRecord }[] = [];
    const seen = new Set<number>();
    for (;;) {
      if (seen.has(from)) throw new Error("repeated record cursor");
      seen.add(from);
      const page = await this.client.records(node, from, 1000);
      out.push(...page.records);
      if (!page.records.length || page.next === from) return out;
      from = page.next;
    }
  }
  async authorCursor(file: OpenFile): Promise<{ sequence: number; previous: string }> {
    const info = await this.client.node(file.manifest.node);
    let sequence = 0, previous = "";
    const snap = await this.snapshotCursor(file.manifest.node, info);
    if (snap) ({ sequence, previous } = snap);
    const from = info.trimmed_before && info.trimmed_before > 1 ? info.trimmed_before : 1;
    const key = fromBase64(this.client.signer.publicKey.replace(/^ed25519:/, ""));
    for (const item of await this.recordsFrom(file.manifest.node, from)) {
      if (item.record.author !== this.client.signer.identity) continue;
      if (sequence === 0 && item.record.sequence !== 1) verifyAppendRecord(item.record, key);
      else verifyNextRecord(item.record, key, sequence, previous);
      sequence = item.record.sequence;
      previous = appendRecordHash(item.record);
    }
    return { sequence, previous };
  }
  private async snapshotCursor(log: string, info: { trim_snapshot?: { node: string; version: string } }): Promise<{ sequence: number; previous: string } | undefined> {
    if (!info.trim_snapshot) return;
    const file = await this.open(info.trim_snapshot.node);
    const bytes = await readAll(this.read(file));
    const doc = JSON.parse(new TextDecoder().decode(bytes)) as { format?: number; log?: string; cursors?: Record<string, { sequence: number; previous: string }> };
    if (doc.format !== 1 || doc.log !== log) return;
    return doc.cursors?.[this.client.signer.identity];
  }
  async append(file: OpenFile, plaintext: Uint8Array): Promise<number> {
    if (file.manifest.mode !== "append" || !file.contentKey) throw new Error("not an append file");
    if (!plaintext.length) throw new Error("empty append");
    const cursor = await this.authorCursor(file);
    let record: AppendRecord = { format: 1, drive: this.client.drive, node: file.manifest.node, author: this.client.signer.identity,
      generation: file.manifest.generation, sequence: cursor.sequence + 1, previous: cursor.previous, chunks: [], signature: "" };
    const blobs: Uint8Array[] = [];
    for (let offset = 0; offset < plaintext.length; offset += MAX_PLAINTEXT) {
      blobs.push(encryptChunk(file.contentKey, plaintext.subarray(offset, offset + MAX_PLAINTEXT), this.context(file.manifest, "content")));
    }
    record.chunks = await this.client.store(blobs);
    record.signature = toBase64url(await this.keys.sign(canonicalAppendRecord(record)));
    const result = await this.client.commit({ records: [record] });
    const position = result.positions?.[0];
    if (position == null) throw new Error("append returned no position");
    return position;
  }
  async tail(file: OpenFile, from = 0): Promise<{ position: number; author: string; sequence: number; plain: Uint8Array }[]> {
    if (file.manifest.mode !== "append" || !file.contentKey) throw new Error("not an append file");
    const info = await this.client.node(file.manifest.node);
    if (!from) from = 1;
    if ((info.trimmed_before ?? 0) > from) throw new Error("record prefix was trimmed; load the snapshot");
    const cursors = new Map<string, { sequence: number; previous: string }>();
    const out = [];
    for (const item of await this.recordsFrom(file.manifest.node, from)) {
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
    for (const ref of record.chunks) parts.push(decryptChunk(file.contentKey!, await this.client.chunk(file.manifest.node, ref), this.context(file.manifest, "content")));
    const size = parts.reduce((sum, part) => sum + part.length, 0);
    const out = new Uint8Array(size);
    let offset = 0;
    for (const part of parts) { out.set(part, offset); offset += part.length; }
    return out;
  }
  async shareWith(file: OpenFile, member: string, memberPublic: Uint8Array, role: ShareRole, expires = ""): Promise<Share> {
    return this.grant(file, member, "", role, expires, memberPublic);
  }
  async link(file: OpenFile, role: ShareRole, expires = "", password = ""): Promise<{ share: Share; fragment: Uint8Array }> {
    const link = id();
    const fragment = randomBytes(32);
    let secret = fragment, salt: Uint8Array | undefined, verifier: Uint8Array | undefined;
    if (password) {
      salt = randomBytes(16);
      const derived = argon2id(utf8(password), salt, { t: 3, m: 65536, p: 1, dkLen: 64 });
      secret = linkSecret(fragment, derived.subarray(0, 32));
      verifier = derived.subarray(32);
    }
    const share = await this.grant(file, "", link, role, expires, x25519PublicKey(secret), salt, verifier);
    return { share, fragment };
  }
  private async grant(file: OpenFile, member: string, link: string, role: ShareRole, expires: string, recipient: Uint8Array, salt?: Uint8Array, verifier?: Uint8Array): Promise<Share> {
    const share: Share = { format: 1, drive: this.client.drive, id: id(), node: file.manifest.node, ...(member ? { member } : { link }), role,
      generation: file.manifest.generation, node_public: toBase64url(x25519PublicKey(file.nodeKey)), ...(expires ? { expires } : {}), caps: {},
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
