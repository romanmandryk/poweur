/** Durable browser outbox for direct messages (EPIC-009 E09-T6). */

const PREFIX = "poweur.outbox.v1.";

export function outboxKey(identity) {
  return `${PREFIX}${String(identity).toLowerCase()}`;
}

export function loadWebOutbox(identity, storage = globalThis.localStorage) {
  try {
    return JSON.parse(storage.getItem(outboxKey(identity)) || "[]");
  } catch {
    return [];
  }
}

function save(identity, entries, storage) {
  storage.setItem(outboxKey(identity), JSON.stringify(entries));
}

export function queueWebMessage(identity, recipient, sealed, options = {}, error = "offline", storage = globalThis.localStorage) {
  const entries = loadWebOutbox(identity, storage);
  const now = Date.now();
  const entry = {
    id: `out_${now}_${globalThis.crypto?.randomUUID?.() ?? Math.random().toString(36).slice(2)}`,
    recipient, payload: sealed.payload, encryption: sealed.encryption,
    options, attempts: 0, next_attempt: now,
    created_at: new Date(now).toISOString(), last_error: String(error),
  };
  entries.push(entry);
  save(identity, entries, storage);
  return entry;
}

export function isRetryableSendError(error) {
  const status = Number(error?.status ?? 0);
  if (status !== 0) return status === 429 || status >= 500;
  return error instanceof TypeError || /failed to fetch|network|offline/i.test(String(error?.message ?? error));
}

export function webRetryDelay(attempt) {
  return Math.min(15 * 60_000, 1000 * (2 ** Math.min(Math.max(attempt, 1), 10)));
}

export async function retryWebOutbox(identity, send, { force = false, storage = globalThis.localStorage, now = Date.now() } = {}) {
  const entries = loadWebOutbox(identity, storage);
  const keep = [];
  let sent = 0;
  for (const entry of entries) {
    if (!force && entry.next_attempt > now) {
      keep.push(entry);
      continue;
    }
    try {
      await send(entry);
      sent += 1;
    } catch (error) {
      if (!isRetryableSendError(error)) continue;
      entry.attempts += 1;
      entry.last_error = error.message ?? String(error);
      entry.next_attempt = now + webRetryDelay(entry.attempts);
      keep.push(entry);
    }
  }
  save(identity, keep, storage);
  return { sent, pending: keep.length };
}
