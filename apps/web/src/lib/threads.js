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

import { conversationKey, describeMessage, expiryCountdown, isEmailType, normalizeMessageType, normalizeSubject, MSG_TYPE_CHAT_ATTACHMENT, MSG_TYPE_CHAT_TEXT, MSG_TYPE_CONTACT_ACCEPT, MSG_TYPE_CONTACT_REQUEST, MSG_TYPE_SHARE_ACCEPT, MSG_TYPE_SHARE_OFFER, MSG_TYPE_SHARE_REVOKED } from "@poweur/client";

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
  if (normalizeMessageType(message.type) === MSG_TYPE_CHAT_ATTACHMENT) {
    let name = message.metadata?.attachment_name || "";
    try {
      const parsed = JSON.parse(message.plaintext);
      if (parsed?.name) name = parsed.name;
    } catch { /* older clients put the label in metadata */ }
    return `📎 ${name || "Attachment"}`;
  }
  return describeMessage(message.sender ?? "", message.type, message.plaintext);
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
 * A message's grouping key inside its conversation (E09-T13): its thread,
 * else `subj:<normalized subject>`, else the default thread "".
 */
export function messageThreadKey(message) {
  return conversationKey(message.thread_id || "", message.subject || "");
}

/**
 * Every message in one conversation, oldest first — the order the store
 * already holds them in. `threadKey` narrows it to one thread (the default
 * thread is ""); `null` is the whole conversation, as the By-ID view shows it.
 *
 * @param {any[]} messages
 * @param {string} selfIdentity
 * @param {string} peer
 * @param {string|null} [threadKey]
 * @param {{ emailBridge?: string }} [options]
 */
export function threadMessages(messages, selfIdentity, peer, threadKey = "", options = {}) {
  const wanted = String(peer ?? "").toLowerCase();
  const out = [];
  for (const raw of messages) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (!message.sender) continue;
    // Consent gestures, notifications and mail have their own surfaces, never chat.
    if (isActionNotice(message.type) || isBridgeMail(message, options.emailBridge)) continue;
    if (conversationPeer(message, selfIdentity).toLowerCase() !== wanted) continue;
    if (threadKey !== null && messageThreadKey(message) !== (threadKey || "")) continue;
    out.push(message);
  }
  return out;
}

/** Mail from the user's own bridge belongs to the Email tray, not chat. */
export function isBridgeMail(message, emailBridge) {
  return Boolean(emailBridge) && isEmailType(message.type) &&
    String(message.sender ?? "").toLowerCase() === String(emailBridge).toLowerCase();
}

/**
 * The divider label to draw above a message in the By-ID stream: the
 * message's thread label when it differs from the previous message's, else "".
 */
export function subjectDivider(message, previous, group = "", subjects = threadSubjects([])) {
  if (!message) return "";
  const key = messageThreadKey(message);
  if (previous && messageThreadKey(previous) === key) return "";
  if (!previous && (!key || (group && key === group))) return "";
  return threadTitle(key, message.subject || subjects.get(key) || "", group);
}

/** The newest subject seen in each thread — a thread's displayed subject. */
export function threadSubjects(messages) {
  const out = new Map();
  for (const message of messages) if (message.subject) out.set(messageThreadKey(message), message.subject);
  return out;
}

/**
 * The title of a thread row in the By-subject view: its subject,
 * "(no subject)" for the default thread, `#name` for a named thread without
 * one (e.g. started with the CLI's --thread).
 */
export function threadTitle(key, subject, group = "") {
  if (subject) return subject;
  if (!key || (group && key === group)) return "(no subject)";
  if (key.startsWith("subj:")) return key.slice(5);
  const name = group && key.startsWith(`${group}:`) ? key.slice(group.length + 1) : key;
  return `#${threadLabel(name)}`;
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

function isActionNotice(type) {
  const normalized = normalizeMessageType(type);
  return isContactHandshake(normalized) || normalized === "sys.auth.request" ||
    normalized === MSG_TYPE_SHARE_OFFER || normalized === MSG_TYPE_SHARE_ACCEPT || normalized === MSG_TYPE_SHARE_REVOKED;
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
export function buildConversationRows(messages, selfIdentity, unreadFor = () => 0, options = {}) {
  const view = options.view === "subject" ? "subject" : "id";
  const byContact = new Map();
  for (const raw of messages) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    // An unsigned message has nobody to thread under; it belongs to the
    // anonymous tray, which renders it as a different kind of object.
    if (!message.sender) continue;
    if (isActionNotice(message.type) || isBridgeMail(message, options.emailBridge)) continue;
    // A verified fan-out carries the group in signed metadata. File it under
    // that address rather than under whichever member happened to speak.
    const contact = conversationPeer(message, selfIdentity);
    if (!contact) continue;
    if (!byContact.has(contact)) byContact.set(contact, []);
    byContact.get(contact).push(message);
  }

  const rows = [];
  for (const [contact, contactMessages] of byContact) {
    const isGroup = contactMessages.some(message => message.group_verified && message.metadata?.group === contact);
    if (view === "id") {
      // By ID (E09-T13): one row per contact, the conversation one stream.
      const lastMsg = contactMessages.at(-1);
      rows.push({
        contact,
        threadId: null,
        threaded: false,
        messages: contactMessages,
        lastMsg,
        title: "",
        unread: unreadFor(contact),
        preview: lastMsg.subject ? `${lastMsg.subject} — ${previewFor(lastMsg)}` : previewFor(lastMsg),
        group: isGroup || contactMessages.some((message) => message.thread_id === contact || String(message.thread_id ?? "").startsWith(`${contact}:`)),
      });
      continue;
    }
    // By subject: one row per (contact, grouping key), like an inbox.
    const byKey = new Map();
    for (const message of contactMessages) {
      const key = messageThreadKey(message);
      if (!byKey.has(key)) byKey.set(key, []);
      byKey.get(key).push(message);
    }
    const groupName = isGroup ? contact : "";
    for (const [key, threadMsgs] of byKey) {
      const lastMsg = threadMsgs.at(-1);
      const subject = [...threadMsgs].reverse().find((message) => message.subject)?.subject ?? "";
      rows.push({
        contact,
        threadId: key,
        threaded: Boolean(key) && key !== groupName,
        messages: threadMsgs,
        lastMsg,
        title: threadTitle(key, subject, groupName),
        subject,
        unread: options.threadUnread ? options.threadUnread(contact, key) : 0,
        preview: previewFor(lastMsg),
        group: isGroup || key === contact || key.startsWith(`${contact}:`),
      });
    }
  }
  // Timestamps are to the second: on a tie the message later in the store
  // (the one that arrived or was sent last) is the newer.
  const position = new Map();
  messages.forEach((raw, index) => {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (message?.id) position.set(message.id, index);
  });
  return rows.sort((a, b) =>
    (new Date(b.lastMsg.timestamp) - new Date(a.lastMsg.timestamp)) ||
    ((position.get(b.lastMsg.id) ?? 0) - (position.get(a.lastMsg.id) ?? 0)));
}

/**
 * Legacy per-(contact, thread) rows — the tray before subjects, kept for
 * callers that still group by raw thread id.
 */
export function buildThreadRows(messages, selfIdentity, unreadFor = () => 0) {
  const byContact = new Map();
  for (const raw of messages) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (!message.sender) continue;
    if (isActionNotice(message.type)) continue;
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
export function unreadTotal(messages, selfIdentity, conversations = {}, options = {}) {
  const self = String(selfIdentity ?? "").toLowerCase();
  let count = 0;
  for (const raw of messages) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (!message.sender || message.sender.toLowerCase() === self) continue;
    // A request is counted once, on Contacts — not again as a message; mail
    // is counted by the Email tray.
    if (isActionNotice(message.type) || isBridgeMail(message, options.emailBridge)) continue;
    const conversation = String(message.group_verified ? message.metadata?.group : message.sender).toLowerCase();
    if (markCovers(conversations?.[conversation], message.timestamp, message.id ?? "")) continue;
    if (markCovers(options.threads?.[conversation]?.[messageThreadKey(message)], message.timestamp, message.id ?? "")) continue;
    count += 1;
  }
  return count;
}

/**
 * Where a reply goes and what subject the composer shows (E09-T13) — one
 * pure function both views use.
 *
 * @param {{ view: "id"|"subject", origin: "new"|"reply", conversation: any[], threadKey?: string|null,
 *           group?: string, touched?: boolean, typed?: string }} input
 * @returns {{ expanded: boolean, value: string, threadId: string, prefill: string, startsNew: boolean }}
 */
export function composerSubject({ view, origin, conversation = [], threadKey = null, group = "", touched = false, typed = "" }) {
  const defaultThread = group || "";
  const newest = conversation.at(-1);
  let expanded = false;
  let prefill = "";
  let threadId = defaultThread;
  if (origin === "new") {
    expanded = view === "subject";
  } else if (view === "subject" && threadKey !== null) {
    // Inside one thread: its subject (or none for the default thread).
    expanded = true;
    prefill = [...conversation].reverse().find((message) => message.subject)?.subject ?? "";
    threadId = threadKey && !threadKey.startsWith("subj:") ? threadKey : defaultThread;
  } else if (newest) {
    // The newest message, whoever sent it, sets the reply's thread.
    threadId = newest.thread_id || defaultThread;
    if (newest.subject) {
      expanded = true;
      prefill = newest.subject;
    }
  }
  const value = touched ? typed : prefill;
  const changed = touched && normalizeSubject(value) !== normalizeSubject(prefill);
  return { expanded: expanded || (touched && Boolean(typed)), value, threadId, prefill, startsNew: changed };
}

/**
 * The thread a send goes to: a changed subject starts a new thread, a
 * cleared one goes to the default thread, an unchanged one stays put.
 */
export function sendThread({ subject, prefill, threadId, group = "", mint }) {
  const typed = String(subject ?? "").trim();
  if (normalizeSubject(typed) === normalizeSubject(prefill ?? "")) return { subject: typed, threadId: threadId ?? "" };
  if (!typed) return { subject: "", threadId: group || "" };
  return { subject: typed, threadId: group ? `${group}:${mint()}` : mint() };
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
