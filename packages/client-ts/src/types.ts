/**
 * Wire types. These mirror the Go structs the relay serves and accepts;
 * field names are the JSON names, not Go names, so a value can go straight
 * onto the wire.
 */

/** Bumped when the wire format changes; exported for capability negotiation. */
export const PROTOCOL_VERSION = 1;

export interface EncryptionMeta {
  alg: string;
  ephemeral_public_key: string;
  nonce: string;
}

/** AEAD identifier for the one scheme v1 defines. */
export const ENCRYPTION_ALG = "x25519-chacha20-poly1305";

/**
 * Self-contained proof that a session key was authorized by an identity, so
 * a relay that never issued the session can still verify a message.
 */
export interface SessionProof {
  session_public_key: string;
  issued_at: string;
  expires_at: string;
  nonce: string;
  identity_signature: string;
}

export interface Message {
  id: string;
  sender: string;
  recipient: string;
  timestamp: string;
  payload: string;
  signature: string;
  /**
   * Envelope-level type; bound into the signature. Absent means
   * `chat.text` — see `normalizeMessageType`. `sys.*` is reserved for the
   * platform and a relay refuses an unregistered one.
   */
  type?: string;
  session_id?: string;
  session_proof?: SessionProof;
  encryption?: EncryptionMeta;
  /**
   * Groups messages into a conversation thread (EPIC-009 E09-T3). Opaque to
   * the relay: it never invents one and never rewrites one. Signed.
   */
  thread_id?: string;
  /**
   * When the sender says this message stops being meaningful (RFC3339).
   * Signed and carried; refusing delivery past it is E09-T6.
   */
  expires_at?: string;
  /**
   * Small, flat, signed and **plaintext** — addressing, not content. Values
   * are strings so two implementations cannot disagree about how to
   * serialize the map for signing. Put nothing private here.
   */
  metadata?: Record<string, string>;
}

/** A message from the inbox, with the decrypt attempt folded in. */
export interface InboxMessage extends Message {
  plaintext: string | null;
  decryptError?: string;
}

export const ACK_TYPE_DELIVERY = "ack";
export const ACK_STATE_DELIVERED_CLIENT = "delivered_client";

export interface Ack {
  type: string;
  id: string;
  message_id: string;
  state: string;
  sender: string;
  recipient: string;
  timestamp: string;
  signature: string;
  session_id?: string;
  session_proof?: SessionProof;
}

export interface InboxResponse {
  messages: Message[];
  acks: Ack[];
  /** Present on a `?since=` pickup: acknowledge it with `consume`. */
  cursor?: string;
  ack_cursor?: string;
  /** How many messages are still spooled after this read. */
  pending?: number;
}

export interface PreviousKey {
  public_key: string;
  valid_until?: string;
}

export interface IdentityDocument {
  version: number;
  identity: string;
  public_key: string;
  encryption_public_key?: string;
  relay: string;
  capabilities?: string[];
  previous_keys?: PreviousKey[];
  moved_to?: string;
  updated_at: string;
  signature?: string;
}

export type ResolveSource = "web" | "dns" | "both";

export interface ResolveResult {
  document: IdentityDocument;
  source: ResolveSource;
}

export interface IdentityResponse {
  identity: string;
  public_key: string;
  encryption_public_key?: string;
  relay: string;
  created_at?: string;
  identity_document?: IdentityDocument | string;
}

export interface HealthResponse {
  status: string;
  version: string;
  buildTime?: string;
  versionHash?: string;
  storage?: {
    configured: boolean;
    path?: string;
    writable: boolean;
    free_bytes?: number;
    error?: string;
  };
}

export interface DavTokenResponse {
  token: string;
  identity: string;
  audience: string;
  scope: string;
  expires_at: string;
}

export interface ShareAudience {
  id?: string;
  group?: string;
  /** Capability-URL token for a public-link share (E05-T4). */
  link?: string;
}

export const PERM_READ = "read";
export const PERM_WRITE = "write";

/**
 * Options that only make sense for a link share (E05-T4). They are part of
 * the canonical signing string, so the relay that stores the grant cannot
 * strip the password or the download cap off it.
 */
export interface ShareLink {
  /** PHC-format argon2id hash — never a plaintext password. */
  password?: string;
  /** Cap on successful downloads through the link; 0/absent = unlimited. */
  max_downloads?: number;
}

export interface ShareGrant {
  share_id: string;
  owner: string;
  path: string;
  audience: ShareAudience[];
  permissions: string[];
  created_at: string;
  expires_at?: string;
  /** Set only on link-share grants (audience = one link token). */
  link?: ShareLink;
  signature: string;
}

/**
 * A signed member list. The same document covers both kinds of group:
 *
 * - **Owner-local** — `group` is a bare name like "team", stored at
 *   `poweur-sys/relay/groups/<name>.json` in the owner's tree and signed by
 *   the owner. It means something only inside that owner's grants.
 * - **Group identity** (E05-T5) — `group` and `owner` are both the group's
 *   own Poweur ID, stored at `GROUP_SELF_DOC` in the *group's* tree and
 *   signed by the group's identity key. It is addressable: any owner can
 *   name it in a grant.
 */
export interface ShareGroup {
  group: string;
  owner: string;
  members: string[];
  /** Identities entitled to update membership; present only on group identities. */
  admins?: string[];
  /** Monotonic membership version; EPIC-009 group keys bind to it. */
  epoch?: number;
  updated_at: string;
  signature: string;
}

export const CONTACT_REQUESTED = "requested";
export const CONTACT_ACCEPTED = "accepted";
export const CONTACT_BLOCKED = "blocked";

export type ContactState =
  | typeof CONTACT_REQUESTED
  | typeof CONTACT_ACCEPTED
  | typeof CONTACT_BLOCKED;

export interface Contact {
  identity: string;
  state: ContactState;
  pinned_key?: string;
  petname?: string;
  tags?: string[];
  added_at?: string;
  source?: string;
}

export interface ContactsFile {
  version: number;
  contacts: Contact[];
}

export const INBOX_OPEN = "open";
export const INBOX_CONTACTS_ONLY = "contacts_only";
export const INBOX_CONTACTS_AND_REQUESTS = "contacts_and_requests";
export const DEFAULT_INBOX_MODE = INBOX_OPEN;

export type InboxMode =
  | typeof INBOX_OPEN
  | typeof INBOX_CONTACTS_ONLY
  | typeof INBOX_CONTACTS_AND_REQUESTS;

export const ANON_CHALLENGE_NONE = "none";
export const ANON_CHALLENGE_POW = "pow";
export const ANON_CHALLENGE_VERIFIED = "verified";
export const ANON_CHALLENGE_PAYMENT = "payment";

export interface AnonymousPolicy {
  allow: boolean;
  challenge?: string;
  pow_bits?: number;
  max_bytes?: number;
  max_per_day?: number;
}

/** Why a handle is (un)available — the relay's `reason` vocabulary. */
export type HandleReason =
  | "available"
  | "taken"
  | "reserved"
  | "blocked"
  | "too_short"
  | "too_long"
  | "charset"
  | "hyphen"
  | "punycode"
  | "domain_not_hosted"
  | "invalid";

/**
 * `GET /` — the relay's service banner.
 *
 * A client reads it to learn which front door it is behind (EPIC-015 E15-T7):
 * the same static tree is served on the launcher host, on a hosted identity's
 * own origin, and from a native shell, and only the host distinguishes them.
 */
export interface RelayRoot {
  service?: string;
  relay_address?: string;
  /** The canonical launcher host — what a post-claim hand-off targets. */
  launcher_host?: string;
  /**
   * Every host that serves the claim flow. Absent on relays older than
   * E15-T7, where `launcher_host` is the whole set.
   */
  launcher_hosts?: string[];
  hosted_domains?: string[];
  web_ui?: string;
  /** Relay semver (EPIC-013 E13-T6). */
  version?: string;
  /** UTC `YYYY-MM-DD HH:MM` when the binary was built. */
  buildTime?: string;
  /** Git revision the binary was built from. */
  versionHash?: string;
}

/** `GET /hosted/availability` (EPIC-018 E18-T2). */
export interface HandleAvailability {
  handle: string;
  identity: string;
  available: boolean;
  reason: HandleReason;
  message: string;
  policy: { min_len: number; max_len: number; charset: string };
}

/** One labeled URL on a profile. */
export interface ProfileLink {
  label?: string;
  url: string;
}

/** `poweur-sys/public/profile.json` (identity.Profile). */
export interface Profile {
  version: number;
  display_name?: string;
  /** A path into the identity's own /public root — never an external URL. */
  avatar?: string;
  bio?: string;
  links?: ProfileLink[];
  locale?: string;
}

export interface InboxPolicy {
  version: number;
  mode: InboxMode;
  anonymous?: AnonymousPolicy;
}

/** System message types the relay routes on (EPIC-007). */
export const MSG_TYPE_CONTACT_REQUEST = "sys.contact.request";
export const MSG_TYPE_CONTACT_ACCEPT = "sys.contact.accept";
export const MSG_TYPE_CONTACT_BLOCK = "sys.contact.block";

export interface ContactRequestEntry {
  id: string;
  sender: string;
  recipient: string;
  timestamp: string;
  type?: string;
  payload: string;
  /** Present because the queued envelope is E2E-encrypted like any message. */
  encryption?: EncryptionMeta;
  /**
   * The decrypted intro, when a decryptor was supplied and it opened. Null
   * when it could not be read; absent when nobody tried.
   */
  plaintext?: string | null;
}

export interface AnonQueueMessage {
  id: string;
  timestamp: string;
  payload: string;
  encryption?: EncryptionMeta;
}

/** One entry in a DAV directory listing. */
export interface DavEntry {
  name: string;
  path: string;
  dir: boolean;
  size: number;
  modified: string;
  etag: string;
}

export interface QuotaResponse {
  used_bytes: number;
  quota_bytes: number;
  provider?: string;
  change_id?: string;
}

/**
 * One row of `poweur-sys/relay/devices.json` (EPIC-004 E04-T6): a machine
 * using this identity, as the relay observed it. Written by the relay and
 * read by the owner; a client never pushes this document.
 */
export interface DeviceEntry {
  id: string;
  name?: string;
  /** laptop | desktop | phone | tablet | browser | agent | unknown. */
  kind?: string;
  added_at?: string;
  last_seen?: string;
  /** Tree prefixes this device syncs; empty means the whole tree. */
  sync_scopes?: string[];
  /** The last changes cursor the device acknowledged. */
  sync_cursor?: string;
  synced_at?: string;
  /** Names of app-passwords.json entries this device holds. */
  app_passwords?: string[];
  revoked?: boolean;
  revoked_at?: string;
}

/** GET /devices/{identity}. */
export interface DeviceListResponse {
  identity: string;
  devices: DeviceEntry[];
}

/** POST /devices/{identity}/revoke — what the revocation actually cost. */
export interface DeviceRevokeResponse {
  device_id: string;
  sessions_revoked: number;
  dav_tokens_revoked: number;
  app_passwords_revoked: number;
}

/** One line of the sync manifest / changes feed. */
export interface SyncEntry {
  path: string;
  dir?: boolean;
  size?: number;
  sha?: string;
  etag?: string;
  modified?: string;
  deleted?: boolean;
}

export interface SyncChange extends SyncEntry {
  op?: string;
}
