/**
 * Arriving from someone's identity page (EPIC-012).
 *
 * `?to=bob.poweur.net` asks to open a chat with Bob. The launcher keeps it
 * through a claim or an "I already have an ID" hop; the identity host opens
 * the chat once the keys are unlocked.
 *
 * The identity page on `bob.poweur.net` cannot read this app's storage on
 * `alice.poweur.net`: every identity is its own origin. So each identity host
 * adds its name to one cookie on the shared parent domain, and Bob's page
 * reads it to offer "Message as alice.poweur.net". The cookie holds only
 * public IDs; the relay never reads it.
 */

const PENDING_KEY = "poweur.pendingChat";
export const ID_HINT_COOKIE = "poweur_ids";
const HINT_MAX_AGE = 400 * 24 * 60 * 60;
const MAX_HINTS = 5;

/** A plausible identity host: lowercase labels, at least one dot. */
export function validChatTarget(value: string): string {
  const id = value.trim().toLowerCase().replace(/\.$/, "");
  if (id.length > 253) return "";
  return /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/.test(id) ? id : "";
}

function session(): Storage | null {
  try {
    return globalThis.sessionStorage ?? null;
  } catch {
    return null;
  }
}

/**
 * Take `?to=` out of the address bar and keep it for this tab. Called once at
 * boot, before anything routes.
 */
export function takeChatTarget(): string {
  const location = globalThis.location;
  if (!location) return pendingChatTarget();
  const url = new URL(location.href);
  const raw = url.searchParams.get("to");
  if (raw === null) return pendingChatTarget();
  url.searchParams.delete("to");
  try {
    history.replaceState(history.state, "", url.pathname + url.search + url.hash);
  } catch {
    /* the target still works */
  }
  const target = validChatTarget(raw);
  if (target) session()?.setItem(PENDING_KEY, target);
  return target;
}

export function pendingChatTarget(): string {
  return validChatTarget(session()?.getItem(PENDING_KEY) ?? "");
}

export function clearPendingChatTarget() {
  session()?.removeItem(PENDING_KEY);
}

/** `?to=` for a link that carries the pending chat to the next host. */
export function chatTargetQuery(): string {
  const target = pendingChatTarget();
  return target ? `?to=${encodeURIComponent(target)}` : "";
}

/** The IDs in the shared cookie, newest first. */
export function readIdHints(cookie: string = globalThis.document?.cookie ?? ""): string[] {
  for (const part of cookie.split(";")) {
    const [name, ...rest] = part.trim().split("=");
    if (name !== ID_HINT_COOKIE) continue;
    let value = rest.join("=");
    try {
      value = decodeURIComponent(value);
    } catch {
      return [];
    }
    return [...new Set(value.split("|").map(validChatTarget).filter(Boolean))].slice(0, MAX_HINTS);
  }
  return [];
}

function writeIdHints(ids: string[], domain: string | undefined) {
  const document = globalThis.document;
  if (!document || !domain) return;
  const secure = globalThis.location?.protocol === "https:" ? "; Secure" : "";
  const attrs = `; Domain=${domain}; Path=/; SameSite=Lax${secure}`;
  document.cookie = ids.length
    ? `${ID_HINT_COOKIE}=${encodeURIComponent(ids.join("|"))}${attrs}; Max-Age=${HINT_MAX_AGE}`
    : `${ID_HINT_COOKIE}=${attrs}; Max-Age=0`;
}

/** This identity is used in this browser: list it first. */
export function rememberIdHint(identity: string, domain: string | undefined) {
  const id = validChatTarget(identity);
  if (!id || !domain || !id.endsWith(`.${domain}`)) return;
  writeIdHints([id, ...readIdHints().filter((other) => other !== id)].slice(0, MAX_HINTS), domain);
}

export function forgetIdHint(identity: string, domain: string | undefined) {
  if (!domain) return;
  const id = validChatTarget(identity);
  writeIdHints(readIdHints().filter((other) => other !== id), domain);
}
