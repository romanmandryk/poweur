/**
 * Conversation and thread grouping for the messages tray (EPIC-009 E09-T3).
 *
 * Pure functions over decrypted inbox records, kept out of `app.js` so they
 * can be unit-tested without a DOM: everything here takes messages in and
 * returns rows out, and the caller owns rendering.
 *
 * The protocol rules — what an absent `type` means, how an unknown type is
 * described — live in `@poweur/client` (`msgtypes.ts`, itself the twin of
 * `packages/identity/msgtypes.go`). This module must not restate them.
 */

import { describeMessage, expiryCountdown, normalizeMessageType, MSG_TYPE_CHAT_ATTACHMENT, MSG_TYPE_CHAT_TEXT, MSG_TYPE_CONTACT_ACCEPT, MSG_TYPE_CONTACT_REQUEST } from "@poweur/client";

/**
 * What the tray shows as a conversation's one-line preview.
 *
 * A `chat.text` shows its text. Anything else — an app's own type, a
 * `sys.*` notice — gets the generic "app message from …" line, because the
 * chat UI has no idea how to render an application's payload and showing the
 * raw plaintext would show the user someone else's JSON. A message we could
 * not open shows the padlock whatever its type claims.
 */
export function previewFor(message) {
  const preview = bodyFor(message);
  if (message.plaintext == null) return preview;
  const countdown = expiryCountdown(message.expires_at);
  return countdown ? `${preview} · ⏳ ${countdown}` : preview;
}

/**
 * What a message says, by the same rules as the preview but without the
 * expiry suffix — the conversation view shows the countdown in the bubble's
 * own meta line instead.
 */
export function bodyFor(message) {
  if (message.plaintext == null) return "🔒 Could not decrypt";
  return normalizeMessageType(message.type) === MSG_TYPE_CHAT_ATTACHMENT
    ? `📎 ${message.metadata?.attachment_name || "Attachment"}`
    : describeMessage(message.sender ?? "", message.type, message.plaintext);
}

/**
 * The conversation a message belongs to: the other party, or the group a
 * verified fan-out was addressed to. One definition, so the tray row and the
 * conversation it opens can never disagree about which messages are in it.
 */
export function conversationPeer(message, selfIdentity) {
  return (message.group_verified ? message.metadata?.group : "") ||
    (message.sender === selfIdentity ? message.recipient : message.sender) || "";
}

/** How many messages the conversation view shows at first, and adds per "Load more". */
export const THREAD_PAGE_SIZE = 10;

/**
 * Every message in one (conversation, thread), oldest first — the order the
 * store already holds them in.
 */
export function threadMessages(messages, selfIdentity, peer, threadId = "") {
  const wanted = String(peer ?? "").toLowerCase();
  const out = [];
  for (const raw of messages) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (!message.sender) continue;
    // The handshake is not chat: the tray leaves it out, so the thread does too.
    if (isContactHandshake(message.type)) continue;
    // A sign-in prompt is a notification with its own tray (EPIC-022).
    if (normalizeMessageType(message.type) === "sys.auth.request") continue;
    if (conversationPeer(message, selfIdentity).toLowerCase() !== wanted) continue;
    if ((message.thread_id || "") !== (threadId || "")) continue;
    out.push(message);
  }
  return out;
}

/**
 * The newest `shown` messages of an oldest-first list, and whether anything
 * older is being held back — which is what decides if "Load more" appears.
 */
export function latestWindow(messages, shown) {
  const count = Math.max(0, Math.min(messages.length, shown));
  return {
    visible: messages.slice(messages.length - count),
    hidden: messages.length - count,
    hasMore: messages.length > count,
  };
}

/**
 * The tick a message we sent has earned: `read` beats `delivered` beats
 * `sent`. A relay's `sys.delivery.failed` notice means it gave up holding the
 * message, which outranks any delivery claim for the same id.
 */
export function deliveryState(message, acks) {
  let state = "sent";
  for (const ack of acks ?? []) {
    if (!message.id || ack.message_id !== message.id) continue;
    if (ack.type === "sys.delivery.failed") return "failed";
    if (ack.state === "read") state = "read";
    else if (state !== "read") state = "delivered";
  }
  return state;
}

/** True for the consent handshake — those belong in Requests, never chat. */
function isContactHandshake(type) {
  const normalized = normalizeMessageType(type);
  return normalized === MSG_TYPE_CONTACT_REQUEST || normalized === MSG_TYPE_CONTACT_ACCEPT;
}

/**
 * Group a contact's messages into threads.
 *
 * A message with no `thread_id` belongs to the conversation's default
 * thread — the flat list the app has always shown. Every distinct
 * `thread_id` becomes a row of its own, so a side conversation about one
 * subject stops interleaving with the main one.
 *
 * Returned newest-activity-first, which is the order the tray renders.
 */
export function threadsOf(messages) {
  const byThread = new Map();
  for (const message of messages) {
    const key = message.thread_id || "";
    let group = byThread.get(key);
    if (!group) {
      group = { threadId: key, threaded: Boolean(key), messages: [] };
      byThread.set(key, group);
    }
    group.messages.push(message);
  }
  return [...byThread.values()]
    .map((group) => ({ ...group, lastMsg: group.messages.at(-1) }))
    .sort((a, b) => new Date(b.lastMsg.timestamp) - new Date(a.lastMsg.timestamp));
}

/**
 * Build the tray's rows from the decrypted inbox.
 *
 * One row per (contact, thread). A contact who has never used a thread gets
 * exactly one row, identical to what the tray rendered before threads
 * existed — the unthreaded case is not a special case here, it is the empty
 * thread id.
 *
 * @param {Array<object|string>} messages  inbox records (JSON strings allowed)
 * @param {string} selfIdentity            us, so we can find the other side
 * @param {(contact: string) => number} unreadFor  unread count per contact
 */
export function buildConversationRows(messages, selfIdentity, unreadFor = () => 0) {
  const byContact = new Map();
  for (const raw of messages) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    // An unsigned message has nobody to thread under; it belongs to the
    // anonymous tray, which renders it as a different kind of object.
    if (!message.sender) continue;
    if (isContactHandshake(message.type)) continue;
    if (normalizeMessageType(message.type) === "sys.auth.request") continue;
    // A verified fan-out carries the group in signed metadata. File it under
    // that address rather than under whichever member happened to speak.
    const contact = conversationPeer(message, selfIdentity);
    if (!contact) continue;
    if (!byContact.has(contact)) byContact.set(contact, []);
    byContact.get(contact).push(message);
  }

  const rows = [];
  for (const [contact, contactMessages] of byContact) {
    const threads = threadsOf(contactMessages);
    // The unread count is per contact, not per thread: the read mark the
    // history file keeps is a conversation-level cursor (E09-T1), and
    // inventing a per-thread one here would be a second, disagreeing answer.
    // It rides on the contact's newest thread so the badge appears once
    // rather than once per row.
    const unread = unreadFor(contact);
    threads.forEach((thread, index) => {
      rows.push({
        contact,
        threadId: thread.threadId,
        threaded: thread.threaded,
        messages: thread.messages,
        lastMsg: thread.lastMsg,
        unread: index === 0 ? unread : 0,
        preview: previewFor(thread.lastMsg),
        group: contactMessages.some(message => message.group_verified && message.metadata?.group === contact) ||
          thread.threadId === contact || thread.threadId.startsWith(`${contact}:`),
      });
    });
  }
  return rows.sort((a, b) => new Date(b.lastMsg.timestamp) - new Date(a.lastMsg.timestamp));
}

/**
 * Whether a read mark covers a message. A mark is a *position* (timestamp and
 * id): timestamps are RFC3339 to the second, so two messages a moment apart
 * share one, and a timestamp-only mark would swallow the second.
 */
export function markCovers(mark, timestamp, id) {
  if (!mark?.timestamp) return false;
  if (mark.timestamp !== timestamp) return new Date(mark.timestamp) > new Date(timestamp);
  return (mark.id ?? "") >= id;
}

/**
 * Every unread signed message across all conversations — the number on the
 * Messages nav badge. Same rule the per-conversation count uses: our own
 * messages never count, and a conversation's read mark covers what is behind it.
 *
 * @param {Array<object|string>} messages
 * @param {string} selfIdentity
 * @param {Record<string, {timestamp: string, id?: string}>} conversations  read marks by conversation
 */
export function unreadTotal(messages, selfIdentity, conversations = {}) {
  const self = String(selfIdentity ?? "").toLowerCase();
  let count = 0;
  for (const raw of messages) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (!message.sender || message.sender.toLowerCase() === self) continue;
    // A request is counted once, on Contacts — not again as a message.
    if (isContactHandshake(message.type)) continue;
    const conversation = String(message.group_verified ? message.metadata?.group : message.sender).toLowerCase();
    if (markCovers(conversations?.[conversation], message.timestamp, message.id ?? "")) continue;
    count += 1;
  }
  return count;
}

/**
 * A short label for a thread row. Empty for the default thread, so an
 * unthreaded conversation renders exactly as it did before.
 */
export function threadLabel(threadId) {
  if (!threadId) return "";
  return threadId.length > 24 ? `${threadId.slice(0, 23)}…` : threadId;
}

/** Re-exported so `app.js` has one import for the display rules. */
export { describeMessage, expiryCountdown, normalizeMessageType, MSG_TYPE_CHAT_TEXT };
