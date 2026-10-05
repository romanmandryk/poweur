/**
 * Message text as the carried thread helpers write it. `lib/threads.js` is
 * shared byte-for-byte with the legacy app and marks an attachment, an
 * undecryptable message and a disappearing one with emoji; here they are icons.
 */
import type { ReactNode } from "react";
import { Hourglass, Lock, Paperclip, type LucideIcon } from "lucide-react";

const LEADS: Record<string, LucideIcon> = { "📎": Paperclip, "🔒": Lock };
const ICON = "mr-1 inline size-[1em] align-[-0.125em]";
const URL_RE = /https?:\/\/[^\s<>"]+/giu;
// Sentence punctuation after a link is not part of it; a closing bracket is
// only dropped when the link has no opening one.
const TRAILING = /[.,;:!?'"”’»]+$/u;

/** Splits text into plain runs and http(s) links; anything else (javascript:, data:) stays text. */
function linkify(body: string): ReactNode[] {
  const parts: ReactNode[] = [];
  let last = 0;
  for (const m of body.matchAll(URL_RE)) {
    let url = m[0];
    for (;;) {
      const trimmed = url.replace(TRAILING, "");
      const close = trimmed.match(/[)\]]$/u);
      const open = close && (close[0] === ")" ? "(" : "[");
      url = close && !trimmed.includes(open!) ? trimmed.slice(0, -1) : trimmed;
      if (url === trimmed) break;
    }
    try {
      new URL(url);
    } catch {
      continue;
    }
    const at = m.index!;
    if (at > last) parts.push(body.slice(last, at));
    parts.push(
      <a key={at} href={url} target="_blank" rel="noopener noreferrer nofollow" className="underline underline-offset-2 [overflow-wrap:anywhere]">
        {url}
      </a>,
    );
    last = at + url.length;
  }
  if (last < body.length) parts.push(body.slice(last));
  return parts;
}

/**
 * `links` makes http(s) URLs clickable (they open in a new tab, or the system
 * browser inside the mobile shell). Leave it off where the text sits inside
 * something that is itself clickable, such as a conversation row.
 */
export function MessageText({ text, links = false }: { text: string | null | undefined; links?: boolean }) {
  let body = String(text ?? "");
  let countdown: string | null = null;
  const expiring = body.match(/ · ⏳ (.+)$/u);
  if (expiring) {
    countdown = expiring[1];
    body = body.slice(0, expiring.index);
  }
  const lead = body.match(/^(📎|🔒) /u);
  const Lead = lead ? LEADS[lead[1]] : null;
  if (lead) body = body.slice(lead[0].length);
  return (
    <>
      {Lead && <Lead className={ICON} aria-hidden="true" />}
      {links ? linkify(body) : body}
      {countdown && (
        <>
          {" · "}
          <Hourglass className={ICON} aria-hidden="true" />
          {countdown}
        </>
      )}
    </>
  );
}
