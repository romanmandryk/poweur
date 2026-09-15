/**
 * Message text as the carried thread helpers write it. `lib/threads.js` is
 * shared byte-for-byte with the legacy app and marks an attachment, an
 * undecryptable message and a disappearing one with emoji; here they are icons.
 */
import { Hourglass, Lock, Paperclip, type LucideIcon } from "lucide-react";

const LEADS: Record<string, LucideIcon> = { "📎": Paperclip, "🔒": Lock };
const ICON = "mr-1 inline size-[1em] align-[-0.125em]";

export function MessageText({ text }: { text: string | null | undefined }) {
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
      {body}
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
