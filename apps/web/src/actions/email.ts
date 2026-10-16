/**
 * The Email tray's actions (EPIC-023 E23-T6): opt in and out, send through
 * the bridge, keep read marks per mail thread, and open attachments.
 */
import { markThreadRead as markThreadMark, stateCovers, toBase64Data, type EmailBlobRef, type EmailOutAttachment, type EmailSend, type EmailSettings } from "@poweur/client";
import { emailSettingsOf, mailEntries, type MailEntry } from "../lib/email";
import { useData } from "../state/data";
import { useSession } from "../state/session";
import { toast } from "../state/ui";
import { trackAction } from "../lib/observability";
import { loadPolicy } from "./account";
import { mergeMessages, trustedEmailBridge } from "./messages";
import { activeClient, errorMessage, parseMessage } from "./relay";

/** Attachments up to this total ride inside the envelope; more are uploaded. */
const INLINE_BUDGET = 256 * 1024;
export const MAX_EMAIL_ATTACHMENTS_BYTES = 20 * 1024 * 1024;

const setEmail = (patch: Partial<ReturnType<typeof useData.getState>["email"]>) =>
  useData.setState((state) => ({ email: { ...state.email, ...patch } }));

/**
 * Bumped by every opt-in change, so a read that started before one cannot
 * land after it with the old answer.
 */
let generation = 0;

/** Whether email is on, which address, and whether the relay offers a bridge. */
export async function loadEmail({ force = false } = {}) {
  const current = useData.getState().email;
  if ((current.loading && !force) || (current.loaded && !force)) return;
  const client = activeClient();
  if (!client) return;
  const started = generation;
  setEmail({ loading: true });
  try {
    const api = client.email();
    const offered = await api.offeredBridge();
    // Known at once: which bridge's messages are mail must not hang on the
    // rest of this read succeeding.
    if (started === generation) setEmail({ offered });
    const state = await api.state(offered);
    if (started !== generation) {
      setEmail({ offered: offered ?? useData.getState().email.offered, loading: false });
      return;
    }
    const bridge = state.bridge ?? offered;
    const caps = bridge ? await api.capabilities(bridge) : {};
    setEmail({
      enabled: state.enabled, address: state.address, bridge: state.bridge, offered, canSend: caps.send !== false, sendPaused: caps.sendPaused === true,
      loaded: true, loading: false, error: null,
    });
  } catch (error) {
    // Not "loaded": a failed read says nothing about whether email is on,
    // and the next screen that needs it asks again.
    setEmail({ loading: false, loaded: false, error: errorMessage(error) });
  }
}

export async function enableEmail(bridge?: string): Promise<boolean> {
  const client = activeClient();
  if (!client) return false;
  generation += 1;
  try {
    const state = await client.email().enable(bridge ?? useData.getState().email.offered ?? undefined);
    setEmail({ enabled: true, address: state.address, bridge: state.bridge, loaded: true });
    await loadPolicy({ force: true });
    toast(`Your address is ${state.address}`, "success", 6000);
    trackAction("email-enable");
    return true;
  } catch (error) {
    toast(`Could not turn on email: ${errorMessage(error)}`, "error", 7000);
    return false;
  }
}

export async function disableEmail(): Promise<boolean> {
  const client = activeClient();
  if (!client) return false;
  generation += 1;
  try {
    await client.email().disable();
    setEmail({ enabled: false, address: null });
    await loadPolicy({ force: true });
    if (useData.getState().tray === "email") useData.setState({ tray: "inbox" });
    toast("Email turned off", "success");
    trackAction("email-disable");
    return true;
  } catch (error) {
    toast(`Could not turn off email: ${errorMessage(error)}`, "error", 7000);
    return false;
  }
}

/** Our receive filters, from the store (see lib/email emailSettingsOf). */
export function currentEmailSettings(): EmailSettings {
  return emailSettingsOf(useData.getState().messages, trustedEmailBridge(), useSession.getState().identity ?? "");
}

/**
 * Change who may email us: sends the whole new settings to the bridge (which
 * drops filtered mail silently) and keeps the sent copy as our record.
 */
export async function saveEmailSettings(change: (current: EmailSettings) => Omit<EmailSettings, "version" | "updated_at">, done?: string): Promise<boolean> {
  const client = activeClient();
  if (!client) return false;
  try {
    const next = change(currentEmailSettings());
    const sent = await client.email().saveSettings({
      ...(next.receive ? { receive: next.receive } : {}),
      ...(next.allow?.length ? { allow: next.allow } : {}),
      ...(next.block?.length ? { block: next.block } : {}),
    });
    const self = useSession.getState().identity ?? "";
    mergeMessages([{
      id: sent.message.id, sender: self, recipient: sent.message.recipient, timestamp: sent.message.timestamp,
      type: "email.settings", queue: "sent", plaintext: JSON.stringify(sent.payload),
    }]);
    if (done) toast(done, "success");
    trackAction("email-settings");
    return true;
  } catch (error) {
    toast(`Could not save: ${errorMessage(error)}`, "error", 7000);
    return false;
  }
}

/** Block mail from an address or "@domain". */
export function blockEmailSender(entry: string): Promise<boolean> {
  const e = entry.trim().toLowerCase();
  return saveEmailSettings((s) => ({ ...s, block: [...(s.block ?? []).filter((b) => b !== e), e] }), `Mail from ${e} will be dropped`);
}

export function unblockEmailSender(entry: string): Promise<boolean> {
  const e = entry.trim().toLowerCase();
  return saveEmailSettings((s) => ({ ...s, block: (s.block ?? []).filter((b) => b !== e) }), `${e} unblocked`);
}

/** The mail in the store. */
export function currentMail(): MailEntry[] {
  const identity = useSession.getState().identity ?? "";
  return mailEntries(useData.getState().messages, trustedEmailBridge(), identity);
}

/** Whether a received entry is unread (its thread mark does not cover it). */
export function mailUnread(entry: MailEntry, readState = useData.getState().history.readState, bridge = trustedEmailBridge()): boolean {
  if (entry.kind !== "received") return false;
  return !stateCovers({ version: 1, conversations: {}, ...(readState as any) }, bridge, entry.key, entry.timestamp, entry.id);
}

/** Mark a mail thread read: one thread mark per message key in it. */
export async function markMailRead(entries: MailEntry[]) {
  const client = activeClient();
  const bridge = trustedEmailBridge();
  if (!client || !bridge) return;
  const unread = entries.filter((entry) => mailUnread(entry));
  if (!unread.length) return;
  try {
    const store = await client.history();
    let state = await store.readState();
    for (const entry of unread) state = markThreadMark(state, bridge, entry.key, entry.timestamp, entry.id);
    await store.putReadState(state);
    useData.setState((s) => ({ history: { ...s.history, readState: state as any } }));
  } catch (error) {
    console.warn("Could not save mail read marks:", errorMessage(error));
  }
}

export interface OutgoingEmail {
  to: { name?: string; address: string }[];
  cc: { name?: string; address: string }[];
  bcc: { name?: string; address: string }[];
  subject: string;
  text: string;
  html?: string;
  in_reply_to?: string;
  references?: string[];
  files: File[];
}

/** Hand an email to the bridge. Returns the send id, or null on failure. */
export async function sendEmail(draft: OutgoingEmail, setStatus: (text: string) => void): Promise<string | null> {
  const client = activeClient();
  if (!client) {
    toast("Unlock your identity first", "warning");
    return null;
  }
  try {
    const total = draft.files.reduce((sum, f) => sum + f.size, 0);
    if (total > MAX_EMAIL_ATTACHMENTS_BYTES) throw new Error(`Attachments may total ${MAX_EMAIL_ATTACHMENTS_BYTES >> 20} MB`);
    const attachments: EmailOutAttachment[] = [];
    for (const file of draft.files) {
      const bytes = new Uint8Array(await file.arrayBuffer());
      const mime = file.type || "application/octet-stream";
      if (total <= INLINE_BUDGET) {
        attachments.push({ name: file.name || "attachment", mime, size: bytes.length, data: toBase64Data(bytes) });
      } else {
        setStatus(`Uploading ${file.name}…`);
        const up = await client.email().upload(bytes);
        attachments.push({ name: file.name || "attachment", mime, size: bytes.length, upload: up.upload, sha256: up.sha256 });
      }
    }
    setStatus("Sending…");
    const profile = useData.getState().profile.doc;
    const sent = await client.email().send({
      to: draft.to, cc: draft.cc, bcc: draft.bcc, subject: draft.subject, text: draft.text,
      ...(draft.html ? { html: draft.html } : {}),
      ...(profile?.display_name ? { from_name: profile.display_name } : {}),
      ...(draft.in_reply_to ? { in_reply_to: draft.in_reply_to } : {}),
      ...(draft.references?.length ? { references: draft.references } : {}),
      ...(attachments.length ? { attachments } : {}),
    });
    keepSent(sent);
    trackAction("email-send", { outcome: "sent" });
    return sent.sendId;
  } catch (error) {
    toast(`Not sent: ${errorMessage(error)}`, "error", 8000);
    trackAction("email-send", { outcome: "failed" });
    return null;
  }
}

type SentEmail = Awaited<ReturnType<ReturnType<NonNullable<ReturnType<typeof activeClient>>["email"]>["send"]>>;

/** Show a send in the thread at once (the archive has it too, unless lost). */
function keepSent(sent: SentEmail) {
  const self = useSession.getState().identity ?? "";
  mergeMessages([{
    id: sent.message.id, sender: self, recipient: sent.message.recipient, timestamp: sent.message.timestamp,
    type: "email.send", queue: "sent", plaintext: JSON.stringify(sent.payload),
  }]);
  if (sent.lost) toast("Sent, but not saved to your history", "warning", 6000);
}

/**
 * Hand the bridge a send it never answered, again. The send id stays the
 * same, so a bridge that did get the first copy answers with what happened
 * instead of mailing it twice.
 */
export async function resendEmail(send: EmailSend): Promise<boolean> {
  const client = activeClient();
  if (!client) {
    toast("Unlock your identity first", "warning");
    return false;
  }
  try {
    const { version: _version, ...draft } = send;
    keepSent(await client.email().send(draft));
    trackAction("email-send", { outcome: "resent" });
    return true;
  } catch (error) {
    toast(`Not sent: ${errorMessage(error)}`, "error", 8000);
    trackAction("email-send", { outcome: "failed" });
    return false;
  }
}

function saveBytes(bytes: Uint8Array, name: string, mime: string) {
  const url = URL.createObjectURL(new Blob([Uint8Array.from(bytes).buffer], { type: mime || "application/octet-stream" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = name;
  anchor.click();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

/** Bytes of an attachment: inline data, or a blob fetched from the bridge. */
export async function attachmentBytes(attachment: { data?: string; blob?: EmailBlobRef }): Promise<Uint8Array> {
  if (attachment.data) {
    const binary = atob(attachment.data);
    const out = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
    return out;
  }
  if (!attachment.blob) throw new Error("This attachment was too large to keep");
  const client = activeClient();
  if (!client) throw new Error("Unlock your identity first");
  return client.email().blob(attachment.blob);
}

export async function saveAttachment(attachment: { name: string; mime: string; data?: string; blob?: EmailBlobRef }) {
  try {
    saveBytes(await attachmentBytes(attachment), attachment.name, attachment.mime);
  } catch (error) {
    toast(`Could not open the attachment: ${errorMessage(error)}`, "error", 7000);
  }
}

/** Download the original message (.eml). */
export async function saveOriginal(ref: EmailBlobRef, subject: string) {
  try {
    const client = activeClient();
    if (!client) throw new Error("Unlock your identity first");
    const bytes = await client.email().blob(ref);
    saveBytes(bytes, `${(subject || "message").replace(/[^\w.-]+/g, "_").slice(0, 60)}.eml`, "message/rfc822");
  } catch (error) {
    toast(`Could not download the original: ${errorMessage(error)}`, "error", 7000);
  }
}

/** Test seam: the parsed store. */
export const _parse = parseMessage;
