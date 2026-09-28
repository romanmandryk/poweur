/** Encrypted owner file workflows. Names and keys never leave this client. */
import { signBytes, x25519PublicKey, type SealedPayload } from "../crypto/index.js";
import { fromBase64, randomBytes, toBase64url } from "../encoding.js";
import { DriveClient } from "./client.js";
import { driveContext, encryptChunk, decryptChunk, sealKey, openKey, MAX_PLAINTEXT } from "./crypto.js";
import { canonicalManifest, verifyManifest, verifyManifestPages, splitPages, type Manifest, type FileMode } from "./manifest.js";
import { nameHash, sealName, openName, normalizeName } from "./names.js";
import type { DriveSealedPayload, ChunkRef } from "./records.js";

const id = () => Array.from(randomBytes(16), b => b.toString(16).padStart(2, "0")).join("");
const wire = (p: SealedPayload): DriveSealedPayload => ({ ephemeral_public_key: p.ephemeralPublicKey, nonce: p.nonce, ciphertext: p.ciphertext });
const payload = (p: DriveSealedPayload): SealedPayload => ({ ephemeralPublicKey: p.ephemeral_public_key, nonce: p.nonce, ciphertext: p.ciphertext });

export interface FileKeys {
  /** Sign binary canonical bytes without converting them to UTF-8 text. */
  sign(bytes: Uint8Array): Promise<Uint8Array>;
  encryptionPrivateKey: Uint8Array;
}
/** Adapter for CLI/local key custody. Browser callers can supply their own signer. */
export function fileKeys(signingPrivateKey: Uint8Array, encryptionPrivateKey: Uint8Array): FileKeys {
  return { sign: async bytes => signBytes(signingPrivateKey, bytes), encryptionPrivateKey };
}
export interface OpenFile {
  manifest: Manifest;
  /** Decrypted display name; the root has an empty name. */
  name: string;
  nodeKey: Uint8Array;
  contentKey?: Uint8Array;
}

export class DriveFiles {
  constructor(readonly client: DriveClient, private readonly keys: FileKeys) {
    if (client.drive !== client.signer.identity) throw new Error("owner file access requires the owner's drive");
    if (keys.encryptionPrivateKey.length !== 32) throw new Error("invalid encryption key");
  }
  private context(m: Manifest, purpose: string): Uint8Array { return driveContext(m.drive, m.node, purpose, m.generation); }
  private async signed(m: Manifest): Promise<Manifest> {
    m.signature = toBase64url(await this.keys.sign(canonicalManifest(m)));
    this.verify(m);
    return m;
  }
  private verify(m: Manifest): void {
    // Shared-author history needs a separately authorized author resolver. Do
    // not accept an arbitrary key supplied by the same server as the manifest.
    if (m.drive !== this.client.drive || m.author !== this.client.signer.identity) throw new Error("untrusted manifest author or drive");
    verifyManifest(m, fromBase64(this.client.signer.publicKey.replace(/^ed25519:/, "")));
  }
  private async version(node: string, version: string): Promise<Manifest> {
    const m = await this.client.version(node, version);
    if (m.node !== node || m.version !== version) throw new Error("manifest reference mismatch");
    this.verify(m);
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
    const parent = info.folder ? await this.open(info.folder, ancestors) : undefined;
    if (parent && parent.manifest.kind !== "folder") throw new Error("parent is not a folder");
    const parentKey = parent?.nodeKey ?? this.keys.encryptionPrivateKey;
    const nodeKey = openKey(parentKey, payload(keyVersion.node_key!), this.context(keyVersion, "node-key"));
    const name = nameVersion ? openName(parentKey, payload(nameVersion.name!), this.context(nameVersion, "name")) : "";
    if (nameVersion && nameHash(parentKey, name) !== nameVersion.name_hash) throw new Error("name index mismatch");
    const result: OpenFile = { manifest: head, name, nodeKey };
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
    let current = await this.root();
    for (const name of parts) {
      const next = (await this.list(current)).find(child => child.name === name);
      if (!next) throw new Error(`drive path not found: ${name}`);
      current = next;
    }
    return current;
  }
  async create(parent: OpenFile | undefined, name: string, kind: "file" | "folder", bytes = new Uint8Array(), mode: FileMode = "replace"): Promise<OpenFile> {
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
    return { manifest: m, name, nodeKey, ...(contentKey ? { contentKey } : {}) };
  }
  private async upload(m: Manifest, key: Uint8Array, bytes: Uint8Array) {
    const refs: ChunkRef[] = [];
    for (let offset = 0; offset < bytes.length; offset += MAX_PLAINTEXT) {
      refs.push(await this.client.upload(encryptChunk(key, bytes.subarray(offset, offset + MAX_PLAINTEXT), this.context(m, "content"))));
    }
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
    this.verify(file.manifest);
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
    file.manifest = m; file.name = name;
  }
  async remove(file: OpenFile): Promise<void> {
    const m = this.next(file, "remove"); m.count = 0; m.pages = [];
    await this.client.commit({ manifest: await this.signed(m) });
    file.manifest = m;
  }
}
