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
 * The archive lives in the owner-only zone of the owner's own tree:
 *
 *   poweur-sys/private/messages/<YYYY-MM>/<sortkey>-<id>.json   sealed records
 *   poweur-sys/private/messages/read-state.json                 sealed marks
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
import { fromUtf8, toBase64url, utf8 } from "./encoding.js";
import { PoweurError, RelayError } from "./errors.js";
import type { DavClient } from "./files.js";
import { ENCRYPTION_ALG } from "./types.js";

export const HISTORY_DIR = "poweur-sys/private/messages";
export const HISTORY_READ_STATE_PATH = `${HISTORY_DIR}/read-state.json`;
export const HISTORY_VERSION = 1;

/** 512 KB, matching the relay's message cap. */
export const MAX_HISTORY_BODY = 512 * 1024;

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
 * One identity's archive, over DAV. Construct it through
 * `PoweurClient.history()`, which supplies the DAV client and decryptor.
 */
export class MessageHistory {
  readonly dav: DavClient;
  readonly owner: string;
  readonly #decryptor: Decryptor;
  /** Month shards this instance has already ensured exist. */
  readonly #shards = new Set<string>();

  constructor(dav: DavClient, owner: string, decryptor: Decryptor) {
    this.dav = dav;
    this.owner = owner;
    this.#decryptor = decryptor;
  }

  async #seal(doc: unknown): Promise<string> {
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

  async #open<T>(raw: string): Promise<T> {
    const doc = parseSealedDocument(raw);
    const plaintext = open(await this.#decryptor.privateKeyBytes(), {
      ciphertext: doc.ciphertext,
      ephemeralPublicKey: doc.ephemeral_public_key,
      nonce: doc.nonce,
    });
    return JSON.parse(fromUtf8(plaintext)) as T;
  }

  /**
   * WebDAV PUT does not create parent collections, and the month shard is new
   * on the first message of every month. Creating it lazily keeps the common
   * path one request rather than an MKCOL before every write.
   */
  async #ensureShard(shard: string): Promise<void> {
    if (this.#shards.has(shard)) return;
    for (const dir of [HISTORY_DIR, `${HISTORY_DIR}/${shard}`]) {
      try {
        await this.dav.mkdir(dir);
      } catch (error) {
        // 405/409 mean it is already there, which is the happy case.
        if (!(error instanceof RelayError) || (error.status !== 405 && error.status !== 409)) {
          throw error;
        }
      }
    }
    this.#shards.add(shard);
  }

  /** Write one record. Idempotent: the path is a function of the message. */
  async append(record: HistoryRecord): Promise<void> {
    const full: HistoryRecord = { ...record, version: HISTORY_VERSION };
    validateHistoryRecord(full);
    const body = await this.#seal(full);
    const path = historyPath(full.timestamp, full.id);
    try {
      await this.dav.write(path, body);
    } catch {
      await this.#ensureShard(historyShard(full.timestamp));
      await this.dav.write(path, body);
    }
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
      } catch {
        failed += 1;
      }
    }
    return { written, failed };
  }

  /** Every archived record, oldest first. Unreadable files are skipped. */
  async load(): Promise<HistoryRecord[]> {
    const records: HistoryRecord[] = [];
    let shards: string[];
    try {
      shards = (await this.dav.list(HISTORY_DIR))
        .filter((entry) => entry.dir && !entry.path.endsWith(HISTORY_DIR))
        .map((entry) => entry.path);
    } catch (error) {
      // No archive yet is an empty archive, not an error.
      if (error instanceof RelayError && error.status === 404) return records;
      throw error;
    }
    for (const shard of shards) {
      let entries;
      try {
        entries = await this.dav.list(shard);
      } catch {
        continue;
      }
      for (const entry of entries) {
        if (entry.dir || !entry.path.endsWith(".json")) continue;
        if (entry.path.endsWith("read-state.json")) continue;
        try {
          records.push(await this.#open<HistoryRecord>(await this.dav.readText(entry.path)));
        } catch {
          // One corrupt file must not hide the rest of someone's history.
        }
      }
    }
    return sortHistory(records);
  }

  /** Records in one conversation, oldest first. */
  async conversation(peer: string): Promise<HistoryRecord[]> {
    const wanted = peer.trim().toLowerCase();
    return (await this.load()).filter((r) => historyPeer(r, this.owner) === wanted);
  }

  async readState(): Promise<ReadState> {
    const raw = await this.dav.readOptional(HISTORY_READ_STATE_PATH);
    if (raw === null) return { version: HISTORY_VERSION, conversations: {} };
    try {
      const state = await this.#open<ReadState>(raw);
      return { version: HISTORY_VERSION, conversations: state.conversations ?? {} };
    } catch {
      return { version: HISTORY_VERSION, conversations: {} };
    }
  }

  async putReadState(state: ReadState): Promise<void> {
    const body = await this.#seal({ version: HISTORY_VERSION, conversations: state.conversations });
    try {
      await this.dav.write(HISTORY_READ_STATE_PATH, body);
    } catch {
      await this.#ensureShard("unknown");
      await this.dav.write(HISTORY_READ_STATE_PATH, body);
    }
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
