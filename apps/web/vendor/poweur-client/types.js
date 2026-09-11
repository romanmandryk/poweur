/**
 * Wire types. These mirror the Go structs the relay serves and accepts;
 * field names are the JSON names, not Go names, so a value can go straight
 * onto the wire.
 */
/** Bumped when the wire format changes; exported for capability negotiation. */
export const PROTOCOL_VERSION = 1;
/** AEAD identifier for the one scheme v1 defines. */
export const ENCRYPTION_ALG = "x25519-chacha20-poly1305";
export const ACK_TYPE_DELIVERY = "ack";
export const ACK_STATE_DELIVERED_CLIENT = "delivered_client";
export const ACK_STATE_READ = "read";
export const PERM_READ = "read";
export const PERM_WRITE = "write";
export const CONTACT_REQUESTED = "requested";
export const CONTACT_ACCEPTED = "accepted";
export const CONTACT_BLOCKED = "blocked";
export const INBOX_OPEN = "open";
export const INBOX_CONTACTS_ONLY = "contacts_only";
export const INBOX_CONTACTS_AND_REQUESTS = "contacts_and_requests";
export const DEFAULT_INBOX_MODE = INBOX_OPEN;
export const ANON_CHALLENGE_NONE = "none";
export const ANON_CHALLENGE_POW = "pow";
export const ANON_CHALLENGE_VERIFIED = "verified";
export const ANON_CHALLENGE_PAYMENT = "payment";
/** System message types the relay routes on (EPIC-007). */
export const MSG_TYPE_CONTACT_REQUEST = "sys.contact.request";
export const MSG_TYPE_CONTACT_ACCEPT = "sys.contact.accept";
export const MSG_TYPE_CONTACT_BLOCK = "sys.contact.block";
