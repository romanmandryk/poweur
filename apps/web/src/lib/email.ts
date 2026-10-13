/**
 * The Email tray's logic (EPIC-023 E23-T6), kept pure so it is tested
 * without a DOM: which messages are mail, how they thread, what a reply or
 * forward says, and how an email's HTML is made safe to show.
 *
 * Trust rule (PCP-0010): a message is mail only when its type is `email.*`
 * and its sender is the bridge the user's own inbox policy names. Anything
 * else stays in chat as an untrusted app message.
 */
import DOMPurify from "dompurify";
import {
  emailThreadKey,
  normalizeSubject,
  parseEmailPayload,
  type EmailAddress,
  type EmailAttachment,
  type EmailMessage,
  type EmailSend,
  type EmailStatus,
} from "@poweur/client";

export interface MailEntry {
  /** The Poweur message id (the archive's key). */
  id: string;
  timestamp: string;
  kind: "received" | "sent";
  /** Read-state thread key (`mail:<root>`). */
  key: string;
  email?: EmailMessage;
  send?: EmailSend;
  /** The newest delivery status of a sent email. */
  status?: EmailStatus;
}

export interface MailThread {
  key: string;
  subject: string;
  /** Who is in it, other than us: names where known. */
  people: string[];
  entries: MailEntry[];
  last: MailEntry;
  unread: number;
  attachments: boolean;
  /** The weakest authentication verdict among received messages. */
  verdict: string;
  spam: boolean;
}

const lower = (s: unknown) => String(s ?? "").toLowerCase();

/** Pull the mail out of the message store (and statuses keyed by send id). */
export function mailEntries(messages: any[], bridge: string, self: string): MailEntry[] {
  if (!bridge) return [];
  const wanted = lower(bridge);
  const me = lower(self);
  const statuses = new Map<string, EmailStatus>();
  const out: MailEntry[] = [];
  const parsed = messages.map((raw) => (typeof raw === "string" ? JSON.parse(raw) : raw));
  for (const message of parsed) {
    if (message.type !== "email.status" || lower(message.sender) !== wanted) continue;
    const status = parseEmailPayload<EmailStatus>(message.plaintext);
    if (!status) continue;
    const prev = statuses.get(status.send_id);
    if (!prev || prev.at <= status.at) statuses.set(status.send_id, status);
  }
  for (const message of parsed) {
    if (message.type === "email.message" && lower(message.sender) === wanted) {
      const email = parseEmailPayload<EmailMessage>(message.plaintext);
      if (!email) continue;
      out.push({ id: message.id, timestamp: message.timestamp, kind: "received", key: emailThreadKey(email, message.id), email });
    } else if (message.type === "email.send" && lower(message.sender) === me && lower(message.recipient) === wanted) {
      const send = parseEmailPayload<EmailSend>(message.plaintext);
      if (!send) continue;
      const status = statuses.get(send.send_id);
      // A sent message threads with what it answers.
      const key = emailThreadKey(
        { message_id: status?.message_id ?? "", in_reply_to: send.in_reply_to ?? "", references: send.references ?? [] },
        message.id,
      );
      out.push({ id: message.id, timestamp: message.timestamp, kind: "sent", key, send, ...(status ? { status } : {}) });
    }
  }
  return out.sort((a, b) => (a.timestamp === b.timestamp ? (a.id < b.id ? -1 : 1) : a.timestamp < b.timestamp ? -1 : 1));
}

const VERDICT_RANK: Record<string, number> = { failed: 0, unverified: 1, verified: 2 };

/**
 * Group entries into threads, newest activity first. A message that answers
 * one we have joins its thread; replies with a changed subject still thread
 * by their references.
 */
export function mailThreads(entries: MailEntry[], isUnread: (entry: MailEntry) => boolean = () => false): MailThread[] {
  // A thread is known by any of its message ids: a reply whose root we never
  // saw still lands beside the message it answers.
  const keyOf = new Map<string, string>();
  const groups = new Map<string, MailEntry[]>();
  for (const entry of entries) {
    const refs = entry.email ? [...(entry.email.references ?? []), entry.email.in_reply_to ?? ""] : [...(entry.send?.references ?? []), entry.send?.in_reply_to ?? ""];
    let key = entry.key;
    for (const ref of refs) {
      const known = ref && keyOf.get(ref);
      if (known) {
        key = known;
        break;
      }
    }
    const ownId = entry.email?.message_id ?? entry.status?.message_id;
    if (ownId) keyOf.set(ownId, key);
    for (const ref of refs) if (ref && !keyOf.has(ref)) keyOf.set(ref, key);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(entry);
  }
  const threads: MailThread[] = [];
  for (const [key, list] of groups) {
    const last = list.at(-1)!;
    const people = new Set<string>();
    let verdict = "verified";
    let spam = false;
    let attachments = false;
    for (const entry of list) {
      if (entry.email) {
        people.add(entry.email.from.name || entry.email.from.address);
        if ((VERDICT_RANK[entry.email.auth?.verdict] ?? 1) < (VERDICT_RANK[verdict] ?? 1)) verdict = entry.email.auth?.verdict ?? "unverified";
        if (entry.email.auth?.spam === "spam") spam = true;
        if (entry.email.attachments?.some((a) => !a.inline)) attachments = true;
      } else if (entry.send) {
        for (const to of entry.send.to) people.add(to.name || to.address);
        if (entry.send.attachments?.length) attachments = true;
      }
    }
    if (!list.some((entry) => entry.email)) verdict = "verified";
    const subject = [...list].reverse().map((entry) => entry.email?.subject ?? entry.send?.subject ?? "").find(Boolean) ?? "";
    threads.push({
      key, subject, people: [...people], entries: list, last, attachments, verdict, spam,
      unread: list.filter((entry) => entry.kind === "received" && isUnread(entry)).length,
    });
  }
  return threads.sort((a, b) => (a.last.timestamp < b.last.timestamp ? 1 : -1));
}

export function formatAddress(a: EmailAddress): string {
  return a.name ? `${a.name} <${a.address}>` : a.address;
}

/** Parse "Ann <a@x>, b@y" into addresses; throws on anything not an address. */
export function parseAddresses(input: string): EmailAddress[] {
  const out: EmailAddress[] = [];
  for (const raw of input.split(/[,;\n]/)) {
    const part = raw.trim();
    if (!part) continue;
    const m = /^(?:"?([^"<]*?)"?\s*)?<([^<>\s]+@[^<>\s]+)>$/.exec(part);
    const address = (m ? m[2] : part).trim();
    if (!/^[^\s@<>()",;]+@[^\s@<>()",;]+\.[^\s@<>()",;]+$/.test(address)) throw new Error(`"${part}" is not an email address`);
    out.push(m && m[1]?.trim() ? { name: m[1].trim(), address } : { address });
  }
  return out;
}

function quote(text: string): string {
  return text
    .replace(/\s+$/, "")
    .split("\n")
    .map((line) => (line ? `> ${line}` : ">"))
    .join("\n");
}

function strippedSubject(subject: string): string {
  // Keep the original's case, minus any reply markers.
  return subject.replace(/^(\s*(re|fwd|fw|aw|sv)(\[\d+\])?\s*:\s*)+/i, "").trim();
}

export interface Draft {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  text: string;
  in_reply_to?: string;
  references?: string[];
}

/** The draft a reply, reply-all or forward starts from. */
export function replyDraft(email: EmailMessage, mode: "reply" | "all" | "forward", selfAddress: string): Draft {
  const me = lower(selfAddress);
  const when = email.date ? new Date(email.date).toUTCString() : "";
  const from = formatAddress(email.from);
  const base = strippedSubject(email.subject ?? "");
  const body = email.text || htmlToText(email.html ?? "");
  if (mode === "forward") {
    return {
      to: "", cc: "", bcc: "",
      subject: `Fwd: ${base}`,
      text: `\n\n---------- Forwarded message ----------\nFrom: ${from}\nDate: ${when}\nSubject: ${email.subject ?? ""}\nTo: ${(email.to ?? []).map(formatAddress).join(", ")}\n\n${body}`,
    };
  }
  const replyTo = email.reply_to?.length ? email.reply_to : [email.from];
  const to = replyTo.filter((a) => lower(a.address) !== me);
  let cc: EmailAddress[] = [];
  if (mode === "all") {
    const seen = new Set([me, ...to.map((a) => lower(a.address))]);
    cc = [...(email.to ?? []), ...(email.cc ?? [])].filter((a) => {
      const key = lower(a.address);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
  }
  const references = [...(email.references ?? []), ...(email.message_id ? [email.message_id] : [])].slice(-20);
  return {
    to: (to.length ? to : replyTo).map(formatAddress).join(", "),
    cc: cc.map(formatAddress).join(", "),
    bcc: "",
    subject: normalizeSubject(email.subject ?? "") ? `Re: ${base}` : "Re:",
    text: `\n\nOn ${when}, ${from} wrote:\n${quote(body)}\n`,
    ...(email.message_id ? { in_reply_to: email.message_id } : {}),
    ...(references.length ? { references } : {}),
  };
}

/** A rough text version of HTML (for quoting an HTML-only email). */
export function htmlToText(html: string): string {
  if (!html) return "";
  const doc = new DOMParser().parseFromString(html, "text/html");
  doc.querySelectorAll("br").forEach((br) => br.replaceWith("\n"));
  doc.querySelectorAll("p,div,tr,li,h1,h2,h3,h4").forEach((el) => el.append("\n"));
  return (doc.body.textContent ?? "").replace(/\n{3,}/g, "\n\n").trim();
}

// ── Safe HTML ────────────────────────────────────────────────────────────────

/**
 * The content security policy written into every email frame: no scripts,
 * no network except — when the user asks for images — https images.
 */
export function emailCSP(loadImages: boolean): string {
  return [
    "default-src 'none'",
    "style-src 'unsafe-inline'",
    `img-src data: blob:${loadImages ? " https:" : ""}`,
    "font-src data:",
    "form-action 'none'",
  ].join("; ");
}

/**
 * Sanitize an email's HTML for an isolated frame: DOMPurify (the client does
 * not trust the bridge's sanitizer either), `cid:` images mapped to their
 * inline attachments, remote images kept only when `loadImages` (the CSP
 * blocks them otherwise anyway), every link opening in a new tab.
 *
 * Returns the full srcdoc and whether remote images were held back.
 */
export function prepareEmailHtml(
  html: string,
  attachments: EmailAttachment[] = [],
  { loadImages = false }: { loadImages?: boolean } = {},
): { srcdoc: string; blockedImages: boolean } {
  const byCid = new Map<string, EmailAttachment>();
  for (const a of attachments) if (a.content_id) byCid.set(a.content_id.toLowerCase(), a);
  const purify = DOMPurify(window);
  if (!purify.isSupported) throw new Error("this browser cannot sanitize email safely");
  const purified = purify.sanitize(html, {
    WHOLE_DOCUMENT: false,
    FORBID_TAGS: ["style", "link", "meta", "base", "form", "input", "button", "textarea", "select", "iframe", "frame", "object", "embed", "svg", "math", "video", "audio", "source", "track", "script", "noscript"],
    FORBID_ATTR: ["srcset", "background", "poster", "formaction", "action", "ping"],
    ALLOW_DATA_ATTR: false,
  }) as string;
  // The second, independent pass also applies the image and link rules.
  const { html: clean, blockedImages } = enforce(purified, byCid, loadImages);
  const srcdoc =
    `<!doctype html><html><head><meta charset="utf-8">` +
    `<meta http-equiv="Content-Security-Policy" content="${emailCSP(loadImages)}">` +
    `<base target="_blank"><meta name="referrer" content="no-referrer">` +
    `<style>html,body{margin:0;padding:12px;background:#fff;color:#111;font:15px/1.45 -apple-system,system-ui,sans-serif;overflow-wrap:anywhere}img{max-width:100%;height:auto}table{max-width:100%}</style>` +
    `</head><body>${clean}</body></html>`;
  return { srcdoc, blockedImages };
}

const FORBIDDEN = "script,style,link,meta,base,form,input,button,textarea,select,option,iframe,frame,frameset,object,embed,applet,svg,math,video,audio,source,track,noscript,template,portal";

/**
 * A second, independent pass over the sanitizer's output: whatever the
 * sanitizer (or the DOM it ran on) let through, these elements and every
 * event-handler attribute do not reach the frame.
 */
function enforce(html: string, byCid: Map<string, EmailAttachment>, loadImages: boolean): { html: string; blockedImages: boolean } {
  let blockedImages = false;
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, "text/html");
  doc.body.querySelectorAll(FORBIDDEN).forEach((el) => el.remove());
  doc.body.querySelectorAll("*").forEach((el) => {
    if (el.tagName === "A") {
      el.setAttribute("target", "_blank");
      el.setAttribute("rel", "noopener noreferrer");
    }
    if (el.tagName === "IMG") {
      const src = el.getAttribute("src") ?? "";
      if (src.toLowerCase().startsWith("cid:")) {
        const a = byCid.get(src.slice(4).toLowerCase());
        if (a?.data && /^image\//.test(a.mime)) el.setAttribute("src", `data:${a.mime};base64,${a.data}`);
        else el.removeAttribute("src");
      } else if (/^https?:/i.test(src)) {
        if (!loadImages || !src.toLowerCase().startsWith("https:")) {
          blockedImages = true;
          el.removeAttribute("src");
        }
      } else if (src && !/^data:image\/(png|jpeg|gif|webp);base64,/i.test(src)) {
        el.removeAttribute("src");
      }
    }
    // Inline styles may not fetch: a url() in CSS is a tracking pixel.
    const style = el.getAttribute("style");
    if (style && /url\s*\(|expression\s*\(|@import/i.test(style)) el.removeAttribute("style");
    for (const attr of [...el.attributes]) {
      const name = attr.name.toLowerCase();
      const value = attr.value.trim().toLowerCase();
      if (name.startsWith("on") || name === "srcset" || name === "formaction" || name === "action" || name === "ping" || name === "background" ||
        ((name === "href" || name === "src" || name === "xlink:href") && /^(javascript|vbscript|data:text)/.test(value.replace(/\s+/g, "")))) {
        el.removeAttribute(attr.name);
      }
    }
  });
  return { html: doc.body.innerHTML, blockedImages };
}

/** The attributes of the frame an email renders in: no scripts, no forms, no top navigation. */
export const EMAIL_FRAME_SANDBOX = "allow-popups allow-popups-to-escape-sandbox allow-same-origin";

/** How a verdict reads in the UI. */
export function verdictLabel(verdict: string | undefined, spam?: boolean): { label: string; tone: "success" | "warning" | "danger" } {
  if (spam) return { label: "Spam", tone: "danger" };
  if (verdict === "verified") return { label: "Verified sender", tone: "success" };
  if (verdict === "failed") return { label: "Failed authentication", tone: "danger" };
  return { label: "Unverified sender", tone: "warning" };
}

/** A send's state as words. */
export function sendStateLabel(state: string | undefined): string {
  switch (state) {
    case "sent": return "Sent";
    case "accepted": return "Sending…";
    case "deferred": return "Delayed — retrying";
    case "bounced": return "Not delivered";
    case "complained": return "Reported as spam";
    case "refused": return "Not sent";
    default: return "Sending…";
  }
}
