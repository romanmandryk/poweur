/**
 * Client-side message history — the TypeScript twin of
 * `packages/identity/history.go`.
 *
 * The relay's inbox is a spool, not an archive: `GET /messages` drains, and
 * once a client has been handed a message the relay no longer has it. So a
 * client that renders straight off the last pickup shows each message exactly
 * once and loses it on the next reload, with nothing left to re-fetch. This
 * module is where a client writes it down instead.
 *
 * The archive lives in the owner-only, end-to-end encrypted zone
 * `.poweur/private/`. Storage v1 kept it as one sealed file per message in
 * month shards over WebDAV; storage v2 keeps one append file per
 * conversation (EPIC-020 E20-T11), named by a hash of the peer. Records stay
 * sealed to the owner's key inside that encrypted log.
 *
 * Sealed to the owner's *own* X25519 key — the same envelope messages already
 * use, with the owner as their own recipient. That needs no new key custody
 * (every identity has that key), keeps the relay honest about a zone it is
 * only contracted not to read, and lets any enrolled device open the archive.
 *
 * Records are write-once and their path is a pure function of (timestamp, id),
 * so two devices picking up the same message write identical bytes to one
 * path. There is no merge to get wrong.
 */

import { open, seal, sha256Bytes } from "./crypto/index.js";
import type { Decryptor } from "./crypto/keys.js";
import { PADDING_BUCKET } from "./drive/crypto.js";
import type { AuthorCursors, DriveFiles, OpenFile } from "./drive/files.js";
import { fromUtf8, toBase64url, utf8 } from "./encoding.js";
import { PoweurError, RelayError } from "./errors.js";
import { ENCRYPTION_ALG } from "./types.js";

export const HISTORY_DIR = ".poweur/private/messages";
export const HISTORY_READ_STATE_PATH = ".poweur/private/read-state.json";
export const HISTORY_VERSION = 1;

/** 512 KB, matching the relay's message cap. */
export const MAX_HISTORY_BODY = 512 * 1024;

/** What has been read of one conversation log. */
/** Records per conversation read by default when a page is needed. */
const DEFAULT_PAGE = 50;

const sortRecords = (records: HistoryRecord[]) =>
  records.sort((a, b) => a.timestamp.localeCompare(b.timestamp) || a.id.localeCompare(b.id));

interface LogCache {
  /** The last position read. */
  through: number;
  /** The first position read: older ones are fetched by `older()`. */
  oldest: number;
  rows: { position: number; record: HistoryRecord }[];
  cursors: AuthorCursors;
}

/** Pad a sealed document with trailing spaces (JSON ignores them) to the
 * drive's padding bucket, so an inline record hides its length as chunks do. */
function padToBucket(bytes: Uint8Array): Uint8Array {
  const size = Math.ceil(bytes.length / PADDING_BUCKET) * PADDING_BUCKET;
  const out = new Uint8Array(size).fill(0x20);
  out.set(bytes);
  return out;
}

export const HISTORY_QUEUE_INBOX = "inbox";
export const HISTORY_QUEUE_ANONYMOUS = "anonymous";
export const HISTORY_QUEUE_REQUESTS = "requests";
/** The owner's own outbound copy — the relay never hands a sender one back. */
export const HISTORY_QUEUE_SENT = "sent";

export type HistoryQueue =
  | typeof HISTORY_QUEUE_INBOX
  | typeof HISTORY_QUEUE_ANONYMOUS
  | typeof HISTORY_QUEUE_REQUESTS
  | typeof HISTORY_QUEUE_SENT;

/**
 * Every unsigned message threads under one pseudo-peer. An anonymous sender
 * has no name to group by, and inventing one would let two unrelated
 * strangers appear as a single correspondent.
 */
export const ANONYMOUS_PEER = "anonymous";

export interface HistoryRecord {
  version?: number;
  id: string;
  /** Empty for anonymous senders — the archive never invents a name. */
  sender?: string;
  recipient: string;
  /** RFC3339. */
  timestamp: string;
  type?: string;
  /**
   * Carried so a client redrawing from the archive after a reload groups the
   * conversation the way the live inbox did (E09-T3). Optional: a record
   * written before threads existed simply has none.
   */
  thread_id?: string;
  expires_at?: string;
  metadata?: Record<string, string>;
  queue: HistoryQueue;
  /** The decrypted text. Ciphertext nobody holds an ephemeral key for is not an archive. */
  body: string;
}

export interface ReadMark {
  timestamp: string;
  id?: string;
}

export interface ReadState {
  version?: number;
  conversations: Record<string, ReadMark>;
}

export interface SealedDocument {
  version?: number;
  alg: string;
  ephemeral_public_key: string;
  nonce: string;
  ciphertext: string;
}

// ── Paths ────────────────────────────────────────────────────────────────────

/** The month directory a timestamp belongs to; "unknown" when unparseable. */
/** SHA-256 of the normalised peer, so a directory listing does not name correspondents. */
export function historyPeerHash(peer: string): string {
  return Array.from(sha256Bytes(utf8(peer.trim().toLowerCase())), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** Append-file name for one conversation. */
export function historyLogName(peer: string): string {
  return `${historyPeerHash(peer)}.jsonl`;
}

export function historyShard(timestamp: string): string {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return "unknown";
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1, 2)}`;
}

/** Bounds the id half of a filename well below the 255-byte segment limit. */
const MAX_ID_SEGMENT = 96;
const SAFE_ID = /^[A-Za-z0-9_-]+$/;

/**
 * The write-once filename for one record: sort key first so a plain lexical
 * listing is chronological, then the id so two messages in one second cannot
 * collide.
 *
 * The id is chosen by the *sender*, which makes it untrusted input on its way
 * to becoming a path segment. Anything outside the id alphabet is replaced
 * wholesale by a hash rather than escaped: a hash is unarguably one safe
 * segment, and still deterministic, so the write stays idempotent.
 */
export function historyFileName(timestamp: string, id: string): string {
  const date = new Date(timestamp);
  const key = Number.isNaN(date.getTime())
    ? "00000000T000000Z"
    : `${date.getUTCFullYear()}${pad(date.getUTCMonth() + 1, 2)}${pad(date.getUTCDate(), 2)}` +
      `T${pad(date.getUTCHours(), 2)}${pad(date.getUTCMinutes(), 2)}${pad(date.getUTCSeconds(), 2)}Z`;
  return `${key}-${safeIdSegment(id)}.json`;
}

export function historyPath(timestamp: string, id: string): string {
  return `${HISTORY_DIR}/${historyShard(timestamp)}/${historyFileName(timestamp, id)}`;
}

function pad(n: number, width: number): string {
  return String(n).padStart(width, "0");
}

function safeIdSegment(id: string): string {
  if (id.length > 0 && id.length <= MAX_ID_SEGMENT && SAFE_ID.test(id)) return id;
  // Must match Go's `safeIDSegment`: SHA-256, first 12 bytes, base64url.
  return `h-${toBase64url(sha256Bytes(utf8(id)).subarray(0, 12))}`;
}

// ── Records ──────────────────────────────────────────────────────────────────

/** The other party in this record's conversation, from the owner's view. */
export function historyPeer(record: HistoryRecord, owner: string): string {
  if (record.queue === HISTORY_QUEUE_ANONYMOUS || !record.sender) return ANONYMOUS_PEER;
  if (record.sender.toLowerCase() === owner.toLowerCase()) return record.recipient.toLowerCase();
  return record.sender.toLowerCase();
}

export function validateHistoryRecord(record: HistoryRecord): void {
  if (record.version !== undefined && record.version !== 0 && record.version !== HISTORY_VERSION) {
    throw new PoweurError("invalid_argument", `unsupported history record version ${record.version}`);
  }
  if (!record.id?.trim()) {
    throw new PoweurError("invalid_argument", "history record: id is required");
  }
  if (!record.recipient?.trim()) {
    throw new PoweurError("invalid_argument", "history record: recipient is required");
  }
  if (Number.isNaN(new Date(record.timestamp).getTime())) {
    throw new PoweurError("invalid_argument", "history record: timestamp must be RFC3339");
  }
  const queues = [
    HISTORY_QUEUE_INBOX,
    HISTORY_QUEUE_ANONYMOUS,
    HISTORY_QUEUE_REQUESTS,
    HISTORY_QUEUE_SENT,
  ];
  if (!queues.includes(record.queue)) {
    throw new PoweurError("invalid_argument", `history record: invalid queue ${record.queue}`);
  }
  if (record.body.length > MAX_HISTORY_BODY) {
    throw new PoweurError("invalid_argument", `history record: body exceeds ${MAX_HISTORY_BODY} bytes`);
  }
}

/** Oldest first, ties broken on id so two clients agree on the order. */
export function sortHistory(records: HistoryRecord[]): HistoryRecord[] {
  return records.sort((a, b) => {
    if (a.timestamp === b.timestamp) return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
    return newerTimestamp(b.timestamp, a.timestamp) ? -1 : 1;
  });
}

function newerTimestamp(a: string, b: string): boolean {
  const ta = new Date(a).getTime();
  const tb = new Date(b).getTime();
  if (Number.isNaN(ta) || Number.isNaN(tb)) return a > b;
  return ta > tb;
}

// ── Read state ───────────────────────────────────────────────────────────────

/**
 * Whether a mark is at or past a record's position.
 *
 * A timestamp alone will not do: message timestamps are RFC3339 to the
 * second, so two messages a moment apart routinely share one and a mark
 * saying "read through 09:30:00" would swallow the message that arrived at
 * 09:30:00 after you looked. Carrying the id makes the mark comparable with
 * exactly the ordering `sortHistory` imposes.
 */
export function markCovers(mark: ReadMark, timestamp: string, id: string): boolean {
  if (mark.timestamp !== timestamp) return newerTimestamp(mark.timestamp, timestamp);
  return (mark.id ?? "") >= id;
}

/** Advance a peer's read mark; never rewind it. */
export function markRead(
  state: ReadState,
  peer: string,
  timestamp: string,
  id: string,
): ReadState {
  const key = peer.trim().toLowerCase();
  if (!key || !timestamp) return state;
  const conversations = { ...state.conversations };
  const existing = conversations[key];
  if (!existing || !markCovers(existing, timestamp, id)) {
    conversations[key] = { timestamp, id };
  }
  return { version: HISTORY_VERSION, conversations };
}

/** Count records past each peer's mark. Our own sent copies never count. */
export function unreadCounts(
  state: ReadState,
  owner: string,
  records: HistoryRecord[],
): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const record of records) {
    if (record.queue === HISTORY_QUEUE_SENT) continue;
    if (record.sender && record.sender.toLowerCase() === owner.toLowerCase()) continue;
    const peer = historyPeer(record, owner);
    const mark = state.conversations[peer];
    if (mark && markCovers(mark, record.timestamp, record.id)) continue;
    counts[peer] = (counts[peer] ?? 0) + 1;
  }
  return counts;
}

export function parseReadState(raw: string): ReadState {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (cause) {
    throw new PoweurError("invalid_argument", "invalid read-state", { cause });
  }
  const doc = (parsed ?? {}) as ReadState;
  if (doc.version !== undefined && doc.version !== 0 && doc.version !== HISTORY_VERSION) {
    throw new PoweurError("invalid_argument", `unsupported read-state version ${doc.version}`);
  }
  const conversations: Record<string, ReadMark> = {};
  for (const [peer, mark] of Object.entries(doc.conversations ?? {})) {
    if (!peer.trim()) throw new PoweurError("invalid_argument", "read-state: empty peer");
    if (Number.isNaN(new Date(mark?.timestamp ?? "").getTime())) {
      throw new PoweurError("invalid_argument", `read-state: ${peer}: timestamp must be RFC3339`);
    }
    conversations[peer] = mark;
  }
  return { version: HISTORY_VERSION, conversations };
}

export function parseSealedDocument(raw: string): SealedDocument {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (cause) {
    throw new PoweurError("invalid_argument", "invalid sealed document", { cause });
  }
  const doc = (parsed ?? {}) as SealedDocument;
  if (doc.version !== undefined && doc.version !== 0 && doc.version !== HISTORY_VERSION) {
    throw new PoweurError("invalid_argument", `unsupported sealed document version ${doc.version}`);
  }
  if (!doc.alg || !doc.ephemeral_public_key || !doc.nonce || !doc.ciphertext) {
    throw new PoweurError(
      "invalid_argument",
      "sealed document: alg, ephemeral_public_key, nonce and ciphertext are required",
    );
  }
  return doc;
}

// ── The store ────────────────────────────────────────────────────────────────

/**
 * One identity's archive. Construct it through `PoweurClient.history()`,
 * which supplies the decryptor.
 */
export class MessageHistory {
  readonly owner: string;
  readonly #decryptor: Decryptor;
  readonly #files?: DriveFiles;
  #private?: OpenFile;
  #messages?: OpenFile;
  /** Per conversation log: the records read so far and where to resume, so
   * a refresh reads only what was appended since. */
  readonly #logs = new Map<string, LogCache>();
  /** Log files by name from the last listing: paging back needs no listing. */
  readonly #logFiles = new Map<string, OpenFile>();
  /** One read or append per log at a time, so the cache stays in order. */
  readonly #queues = new Map<string, Promise<unknown>>();
  #readStateCache?: { version: string; state: ReadState };

  constructor(owner: string, decryptor: Decryptor, files?: DriveFiles) {
    this.owner = owner;
    this.#decryptor = decryptor;
    this.#files = files;
  }

  /** Seal a document to the owner's own encryption key. */
  async seal(doc: unknown): Promise<string> {
    const sealed = seal(this.#decryptor.encryptionPublicKey, JSON.stringify(doc));
    const envelope: SealedDocument = {
      version: HISTORY_VERSION,
      alg: ENCRYPTION_ALG,
      ephemeral_public_key: sealed.ephemeralPublicKey,
      nonce: sealed.nonce,
      ciphertext: sealed.ciphertext,
    };
    return JSON.stringify(envelope, null, 2);
  }

  /** Open a document sealed with `seal`. */
  async open<T>(raw: string): Promise<T> {
    const doc = parseSealedDocument(raw);
    const plaintext = open(await this.#decryptor.privateKeyBytes(), {
      ciphertext: doc.ciphertext,
      ephemeralPublicKey: doc.ephemeral_public_key,
      nonce: doc.nonce,
    });
    return JSON.parse(fromUtf8(plaintext)) as T;
  }

  /** Write one record. A record already stored under its id (among those
   * read so far: the newest page of each conversation at least) is skipped. */
  async append(record: HistoryRecord): Promise<void> {
    const stored = { ...record, version: HISTORY_VERSION };
    validateHistoryRecord(stored);
    const files = this.#requireFiles();
    const file = await this.#log(historyPeer(record, this.owner));
    const node = file.manifest.node;
    await this.#serial(node, async () => {
      const log = await this.#readLogNow(file, DEFAULT_PAGE);
      if (log.rows.some((row) => row.record.id === record.id)) return;
      // Sealed inline in the record (one request to read back, not one per
      // record) and padded like chunks, so the relay cannot see its length.
      const sealed = padToBucket(utf8(await this.seal(stored)));
      // Our chain position comes from the relay (one request, whatever the
      // log's length); append advances it, and the log's read cursors follow.
      const cursor = await files.authorCursor(file);
      const position = await files.append(file, sealed, { inline: true, cursor });
      if (position === log.through + 1) {
        log.rows.push({ position, record: stored as HistoryRecord });
        log.through = position;
        log.cursors.set(this.owner, cursor);
      } else {
        this.#logs.delete(node); // records we have not read came first
      }
    });
  }

  /**
   * Archive a batch, attempting all of them. One record that will not write is
   * not a reason to lose the rest.
   */
  async appendAll(records: HistoryRecord[]): Promise<{ written: number; failed: number }> {
    let written = 0;
    let failed = 0;
    for (const record of records) {
      try {
        await this.append(record);
        written += 1;
      } catch (error) {
        // "No storage yet" is not a failure to report on every pickup.
        if (error instanceof PoweurError && error.code === "unsupported") continue;
        failed += 1;
      }
    }
    return { written, failed };
  }

  /**
   * Archived records, oldest first: the newest `perConversation` of each
   * conversation on the first load (one request per conversation), then on
   * later loads only what was appended since. `older()` pages back.
   */
  async load(options: { perConversation?: number } = {}): Promise<HistoryRecord[]> {
    if (!this.#files) return [];
    const dir = await this.#messagesDir();
    // One listing says how far each log has grown; only logs with new
    // records are read, and only from where the last read stopped.
    const logs = (await this.#files.list(dir)).filter((child) => child.manifest.mode === "append");
    for (const log of logs) this.#logFiles.set(log.name, log);
    const read = await Promise.all(logs.map((log) => this.#readLog(log, options.perConversation ?? 0)));
    return sortRecords(read.flat().map((row) => row.record));
  }

  /** The newest records of one conversation. `limit` 0 returns the whole log. */
  async tail(peer: string, options: { limit?: number } = {}): Promise<HistoryRecord[]> {
    const limit = options.limit ?? 0;
    const rows = await this.#conversation(peer, limit);
    const slice = limit > 0 ? rows.slice(-limit) : rows;
    return slice.map((row) => row.record);
  }

  /** Records strictly before a relay position, newest `limit` of them;
   * older pages are fetched as needed. */
  async before(peer: string, cursor: number, options: { limit?: number } = {}): Promise<HistoryRecord[]> {
    const limit = options.limit ?? 0;
    let rows = (await this.#conversation(peer, limit || DEFAULT_PAGE)).filter((row) => row.position < cursor);
    while ((limit === 0 || rows.length < limit) && (await this.older(peer, limit ? limit - rows.length : 1000)).length) {
      rows = ((await this.#logFor(peer))?.rows ?? []).filter((row) => row.position < cursor);
    }
    return (limit > 0 ? rows.slice(-limit) : rows).map((row) => row.record);
  }

  /**
   * Read up to `limit` records older than the oldest read so far in one
   * conversation (one request). Returns them oldest first; empty when the
   * start of the conversation is already loaded.
   */
  async older(peer: string, limit = DEFAULT_PAGE): Promise<HistoryRecord[]> {
    const file = await this.#logFile(peer);
    if (!file) return [];
    return this.#serial(file.manifest.node, async () => {
      const log = await this.#readLogNow(file, limit);
      const floor = Math.max(1, file.trimmedBefore ?? 1);
      if (log.oldest <= floor) return [];
      const from = Math.max(floor, log.oldest - limit);
      // A page of its own: chains are checked within it and signatures on each.
      const rows = await this.#requireFiles().tail(file, from, new Map(), log.oldest - from);
      const opened = await Promise.all(rows.map(async (row) => ({
        position: row.position,
        record: await this.open<HistoryRecord>(new TextDecoder().decode(row.plain)),
      })));
      log.rows = [...opened, ...log.rows];
      log.oldest = from;
      return opened.map((row) => row.record);
    });
  }

  /** Whether one conversation has records older than those read so far. */
  async hasOlder(peer: string): Promise<boolean> {
    const file = await this.#logFile(peer);
    const log = file ? this.#logs.get(file.manifest.node) : undefined;
    return Boolean(file && log && log.oldest > Math.max(1, file.trimmedBefore ?? 1));
  }

  /** Records in one conversation, oldest first: the whole log. */
  async conversation(peer: string): Promise<HistoryRecord[]> {
    if (!this.#files) {
      const wanted = peer.trim().toLowerCase();
      return (await this.load()).filter((r) => historyPeer(r, this.owner) === wanted);
    }
    await this.#conversation(peer, 0);
    while ((await this.older(peer, 1000)).length);
    return ((await this.#logFor(peer))?.rows ?? []).map((row) => row.record);
  }

  async readState(): Promise<ReadState> {
    const empty = { version: HISTORY_VERSION, conversations: {} };
    if (!this.#files) return empty;
    await this.#messagesDir();
    const marks = (await this.#files.list(this.#private!)).find((child) => child.name === "read-state.json");
    if (!marks) return empty;
    if (this.#readStateCache?.version === marks.manifest.version) return this.#readStateCache.state;
    const bytes = await collect(this.#files.read(marks));
    if (!bytes.length) return empty;
    const state = parseReadState(JSON.stringify(await this.open<ReadState>(new TextDecoder().decode(bytes))));
    this.#readStateCache = { version: marks.manifest.version, state };
    return state;
  }

  /** Store the read marks. */
  async putReadState(state: ReadState): Promise<void> {
    if (!this.#files) return;
    const sealed = utf8(await this.seal({ ...state, version: HISTORY_VERSION }));
    await this.#messagesDir();
    const marks = (await this.#files.list(this.#private!)).find((child) => child.name === "read-state.json");
    let written: OpenFile;
    if (marks) { await this.#files.replace(marks, sealed); written = marks; }
    else written = await this.#files.create(this.#private, "read-state.json", "file", sealed);
    this.#readStateCache = { version: written.manifest.version, state: parseReadState(JSON.stringify({ ...state, version: HISTORY_VERSION })) };
  }

  #requireFiles(): DriveFiles {
    if (!this.#files) throw new PoweurError("unsupported", "message history needs the drive (EPIC-020), which this client cannot sign");
    return this.#files;
  }
  async #folder(parent: OpenFile, name: string): Promise<OpenFile> {
    const files = this.#requireFiles();
    const found = (await files.list(parent)).find((child) => child.name === name && child.manifest.kind === "folder");
    if (found) return found;
    try { return await files.create(parent, name, "folder"); }
    catch (error) {
      if (!(error instanceof RelayError) || error.status !== 409) throw error;
      const again = (await files.list(parent)).find((child) => child.name === name);
      if (!again) throw error;
      return again;
    }
  }
  async #messagesDir(): Promise<OpenFile> {
    if (this.#messages) return this.#messages;
    const files = this.#requireFiles();
    const poweur = await this.#folder(await files.root(), ".poweur");
    this.#private = await this.#folder(poweur, "private");
    this.#messages = await this.#folder(this.#private, "messages");
    return this.#messages;
  }
  async #log(peer: string): Promise<OpenFile> {
    const files = this.#requireFiles();
    const dir = await this.#messagesDir();
    const name = historyLogName(peer);
    const file = (await files.list(dir)).find((child) => child.name === name) ?? await files.create(dir, name, "file", new Uint8Array(), "append");
    this.#logFiles.set(name, file);
    return file;
  }
  /** Run one step on a log after the ones queued before it. */
  #serial<T>(node: string, step: () => Promise<T>): Promise<T> {
    const next = (this.#queues.get(node) ?? Promise.resolve()).catch(() => {}).then(step);
    this.#queues.set(node, next);
    return next;
  }
  async #readLog(file: OpenFile, newest = 0): Promise<{ position: number; record: HistoryRecord }[]> {
    return (await this.#serial(file.manifest.node, () => this.#readLogNow(file, newest))).rows;
  }
  /** Read a log's records not seen yet, continuing each author's chain. A
   * log read for the first time starts at its newest `newest` records (0:
   * from the beginning). */
  async #readLogNow(file: OpenFile, newest = 0): Promise<LogCache> {
    const node = file.manifest.node;
    let log = this.#logs.get(node);
    if (log && file.position !== undefined && file.position <= log.through) return log;
    if (!log) {
      const floor = Math.max(1, file.trimmedBefore ?? 1);
      const start = newest > 0 && file.position !== undefined ? Math.max(floor, file.position - newest + 1) : floor;
      log = { through: start - 1, oldest: start, rows: [], cursors: new Map() };
    }
    const rows = await this.#requireFiles().tail(file, log.through + 1, log.cursors);
    const opened = await Promise.all(rows.map(async (row) => ({
      position: row.position,
      record: await this.open<HistoryRecord>(new TextDecoder().decode(row.plain)),
    })));
    for (const row of opened) {
      log.rows.push(row);
      log.through = Math.max(log.through, row.position);
    }
    this.#logs.set(node, log);
    return log;
  }
  async #logFile(peer: string, fresh = false): Promise<OpenFile | undefined> {
    const name = historyLogName(peer);
    const known = this.#logFiles.get(name);
    if (known && !fresh) return known;
    const dir = await this.#messagesDir();
    const file = (await this.#requireFiles().list(dir)).find((child) => child.name === name);
    if (file) this.#logFiles.set(name, file);
    return file;
  }
  async #logFor(peer: string): Promise<LogCache | undefined> {
    const file = await this.#logFile(peer);
    return file ? this.#logs.get(file.manifest.node) : undefined;
  }
  async #conversation(peer: string, newest = 0): Promise<{ position: number; record: HistoryRecord }[]> {
    // Fresh: a conversation read asks for what is new in it.
    const file = await this.#logFile(peer, true);
    return file ? this.#readLog(file, newest) : [];
  }

  /** Advance one conversation's mark to the newest record given. */
  async markConversationRead(peer: string, records: HistoryRecord[]): Promise<ReadState> {
    let state = await this.readState();
    for (const record of records) {
      if (record.queue === HISTORY_QUEUE_SENT) continue;
      if (record.sender && record.sender.toLowerCase() === this.owner.toLowerCase()) continue;
      if (historyPeer(record, this.owner) !== peer.trim().toLowerCase()) continue;
      state = markRead(state, peer, record.timestamp, record.id);
    }
    await this.putReadState(state);
    return state;
  }

  /** Unread counts per conversation over the whole archive. */
  async unread(): Promise<Record<string, number>> {
    const [records, state] = await Promise.all([this.load(), this.readState()]);
    return unreadCounts(state, this.owner, records);
  }
}

async function collect(chunks: AsyncGenerator<Uint8Array>): Promise<Uint8Array> {
  const parts: Uint8Array[] = [];
  for await (const chunk of chunks) parts.push(chunk);
  const out = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0));
  let offset = 0;
  for (const part of parts) { out.set(part, offset); offset += part.length; }
  return out;
}
