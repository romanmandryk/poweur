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

import { describeMessage, normalizeMessageType, MSG_TYPE_CHAT_TEXT } from "@poweur/client";

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
  if (message.plaintext == null) return "🔒 Could not decrypt";
  return describeMessage(message.sender ?? "", message.type, message.plaintext);
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
    const contact = message.sender === selfIdentity ? message.recipient : message.sender;
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
      });
    });
  }
  return rows.sort((a, b) => new Date(b.lastMsg.timestamp) - new Date(a.lastMsg.timestamp));
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
export { describeMessage, normalizeMessageType, MSG_TYPE_CHAT_TEXT };
