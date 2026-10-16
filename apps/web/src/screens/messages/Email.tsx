/**
 * Email (EPIC-023 E23-T6): the tray, one mail thread, and the composer.
 *
 * Mail arrives as `email.message` from the user's bridge (on by default); the
 * bridge sanitized its HTML, and this client sanitizes it again and renders it
 * in a sandboxed frame with no scripts and no network (remote images only on
 * request). Nothing here trusts the email.
 */
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { AtSign, Ban, ChevronDown, ChevronUp, Download, FileText, Forward, ImageOff, Mail, Paperclip, Reply, ReplyAll, ShieldAlert, SquarePen, X } from "lucide-react";
import type { EmailAttachment, EmailMessage } from "@poweur/client";
import { attachmentBytes, blockEmailSender, enableEmail, loadEmail, mailUnread, markMailRead, MAX_EMAIL_ATTACHMENTS_BYTES, saveAttachment, saveOriginal, sendEmail } from "../../actions/email";
import { historyHasOlder, loadOlderHistory, trustedEmailBridge } from "../../actions/messages";
import { askConfirm } from "../../components/Dialogs";
import { MessageText } from "../../components/MessageText";
import { cn } from "../../lib/cn";
import {
  EMAIL_FRAME_SANDBOX, formatAddress, mailEntries, mailThreads, parseAddresses, prepareEmailHtml, replyDraft, sendStateLabel, verdictLabel,
  type Draft, type MailEntry, type MailThread,
} from "../../lib/email";
import { fmtRelative } from "../../lib/format";
import { emailBridgeOf, useData } from "../../state/data";
import { useRoute } from "../../state/route";
import { useSession } from "../../state/session";
import { Button } from "../../ui/Button";
import { Chip, CountBadge, EmptyState, Notice } from "../../ui/Display";
import { Input, Label, Textarea } from "../../ui/Field";
import { SubPage } from "../../ui/Layout";

/**
 * The mail threads, computed from store values passed in — never read
 * through getState() inside: the React Compiler memoizes on what the code
 * reads, and a hidden read would freeze the list.
 */
function useMailThreads(): MailThread[] {
  const messages = useData((state) => state.messages);
  const readState = useData((state) => state.history.readState);
  const bridge = useData((state) => emailBridgeOf(state.policy.doc, state.email.offered));
  const identity = useSession((state) => state.identity) ?? "";
  return useMemo(
    () => mailThreads(mailEntries(messages, bridge, identity), (entry) => mailUnread(entry, readState, bridge)),
    [messages, readState, bridge, identity],
  );
}

/** Unread mail, for the tray badge. */
export function useUnreadMail(): number {
  return useMailThreads().reduce((sum, thread) => sum + thread.unread, 0);
}

export function openMailThread(key: string) {
  useData.setState({ mailThread: key });
  useRoute.getState().push("email-thread", { key });
}

export function openCompose(draft?: Partial<Draft>) {
  useRoute.getState().push("email-compose", { draft: draft ?? {} });
}

export function EmailTray() {
  const email = useData((state) => state.email);
  const historyLoaded = useData((state) => state.history.loaded);
  const threads = useMailThreads();
  const [older, setOlder] = useState(false);
  const [paging, setPaging] = useState(false);
  useEffect(() => {
    void loadEmail();
  }, []);
  // The archive opens with the newest page of each conversation; mail is one
  // conversation with the bridge, so older mail pages in on request.
  useEffect(() => {
    const bridge = trustedEmailBridge();
    if (bridge) void historyHasOlder(bridge).then(setOlder);
  }, [historyLoaded, threads.length]);

  if (!email.enabled && email.error && !email.loading) {
    return (
      <EmptyState
        icon={Mail}
        title="Could not load your email settings"
        body={email.error}
        action={<Button id="btn-email-retry" size="sm" variant="secondary" onClick={() => void loadEmail({ force: true })}>Try again</Button>}
      />
    );
  }
  if (!email.enabled && email.loaded) {
    return <EmailOptIn />;
  }
  return (
    <>
      <div className="flex items-center justify-between border-b border-sep bg-surface px-4 py-2">
        <span id="email-address" className="flex min-w-0 items-center gap-1.5 truncate text-[13px] text-muted">
          <AtSign className="size-3.5 shrink-0" aria-hidden="true" />
          {email.address ?? "…"}
        </span>
        {email.canSend && (
          <Button id="btn-email-compose" size="sm" variant="secondary" onClick={() => openCompose()}>
            <SquarePen className="size-4" aria-hidden="true" /> Write
          </Button>
        )}
      </div>
      {!threads.length ? (
        <EmptyState
          icon={Mail}
          title="No email yet"
          body={`Use ${email.address ?? "your address"} to sign up to apps and services that don't offer Sign in with Poweur yet. Their mail lands here, encrypted to you.`}
        />
      ) : (
        <div className="conv-list mail-list bg-surface">
          {threads.map((thread) => (
            <MailRow key={thread.key} thread={thread} />
          ))}
        </div>
      )}
      {older && (
        <div className="flex justify-center py-3">
          <Button
            id="btn-email-older"
            size="sm"
            variant="secondary"
            disabled={paging}
            onClick={async () => {
              const bridge = trustedEmailBridge();
              setPaging(true);
              try {
                await loadOlderHistory(bridge, 50);
                setOlder(await historyHasOlder(bridge));
              } finally {
                setPaging(false);
              }
            }}
          >
            {paging ? "Loading…" : "Load older email"}
          </Button>
        </div>
      )}
    </>
  );
}

function MailRow({ thread }: { thread: MailThread }) {
  const last = thread.last;
  const snippet = (last.email?.text || last.send?.text || "").replace(/\s+/g, " ").trim().slice(0, 140);
  const sendState = last.kind === "sent" ? last.status?.state : undefined;
  const verdict = verdictLabel(thread.verdict, thread.spam);
  const open = () => openMailThread(thread.key);
  return (
    <div
      role="button"
      tabIndex={0}
      data-mail-thread={thread.key}
      onClick={open}
      onKeyDown={(event) => (event.key === "Enter" || event.key === " ") && open()}
      className={cn(
        "mail-row conv-row flex min-h-13 cursor-pointer items-start gap-3 border-b border-sep px-4 py-3 last:border-b-0 active:bg-surface-2 [@media(hover:hover)]:hover:bg-surface-2",
        thread.unread > 0 && "unread",
      )}
    >
      <div className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-2 text-muted">
        <Mail className="size-[18px]" aria-hidden="true" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className={cn("mail-people truncate text-[15px]", thread.unread > 0 ? "font-bold" : "font-semibold")}>
            {thread.people.join(", ") || "(nobody)"}
          </span>
          {thread.entries.length > 1 && <span className="text-xs text-faint">{thread.entries.length}</span>}
        </div>
        <div className={cn("mail-subject truncate text-sm", thread.unread > 0 ? "font-semibold text-fg" : "text-fg")}>
          {thread.subject || "(no subject)"}
        </div>
        <div className="mail-snippet truncate text-[13px] text-muted">{snippet}</div>
        <div className="mt-1 flex flex-wrap gap-1">
          {thread.verdict !== "verified" || thread.spam ? <Chip tone={verdict.tone}>{verdict.label}</Chip> : null}
          {sendState && sendState !== "sent" && <Chip tone={sendState === "accepted" || sendState === "deferred" ? "warning" : "danger"}>{sendStateLabel(sendState)}</Chip>}
        </div>
      </div>
      <div className="flex shrink-0 flex-col items-end gap-[5px]">
        <span className="text-xs text-faint">{fmtRelative(last.timestamp)}</span>
        {thread.attachments && <Paperclip className="size-3.5 text-faint" aria-label="Has attachments" />}
        <CountBadge count={thread.unread} />
      </div>
    </div>
  );
}

/** Offer the address — what it is, what the bridge can see. */
export function EmailOptIn({ compact = false }: { compact?: boolean }) {
  const email = useData((state) => state.email);
  const identity = useSession((state) => state.identity) ?? "";
  const [busy, setBusy] = useState(false);
  const label = identity.split(".")[0];
  const address = identity.includes(".") ? `${label}@${identity.slice(label.length + 1)}` : identity;
  if (!email.offered) {
    return compact ? null : <EmptyState icon={Mail} title="Email is not available" body="Your relay does not run an email bridge." />;
  }
  return (
    <div id="email-optin" className={cn("rounded-card bg-surface px-4 py-5", !compact && "m-4")}>
      <div className="flex items-center gap-2 text-[17px] font-bold">
        <Mail className="size-5 text-accent" aria-hidden="true" /> {address} is off
      </div>
      <p className="mt-2 text-sm text-muted">
        An ordinary email address for your Poweur ID, for the apps and services that don't offer Sign in with Poweur yet: sign up with it, and their mail arrives here, encrypted to you.
      </p>
      <ul className="mt-2 list-disc pl-5 text-[13px] text-muted">
        <li>Email is not end-to-end encrypted on the way: senders' mail servers and the bridge ({email.offered}) see it while it passes.</li>
        {email.canSend && email.sendPaused ? (
          <li>Sending email is paused for now; receiving works as usual.</li>
        ) : email.canSend ? (
          <li>Sending is limited and paced (a few a day) to keep the address off spam lists; the first email you send to someone carries a one-line note about Poweur.</li>
        ) : (
          <li>This address receives mail only — sign-in links, notifications, newsletters.</li>
        )}
        <li>You can turn it off at any time in Settings, or block senders and keep out strangers there.</li>
      </ul>
      <Button
        id="btn-email-enable"
        className="mt-4"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          await enableEmail();
          setBusy(false);
        }}
      >
        {busy ? "Turning on…" : "Turn email back on"}
      </Button>
    </div>
  );
}

// ── One thread ───────────────────────────────────────────────────────────────

export function EmailThreadScreen() {
  const key = useData((state) => state.mailThread);
  const threads = useMailThreads();
  const pop = useRoute((state) => state.pop);
  const thread = threads.find((t) => t.key === key) ?? threads.find((t) => t.entries.some((e) => e.key === key));
  useEffect(() => {
    if (thread?.unread) void markMailRead(thread.entries);
  }, [thread?.unread, thread?.key]);
  if (!thread) {
    return (
      <SubPage title="Email" onBack={pop}>
        <EmptyState icon={Mail} title="Not found" body="This email is no longer here." />
      </SubPage>
    );
  }
  const canSend = useData((state) => state.email.canSend);
  const newestReceived = canSend ? [...thread.entries].reverse().find((e) => e.email)?.email : undefined;
  return (
    <SubPage
      title={<span className="mail-thread-subject">{thread.subject || "(no subject)"}</span>}
      onBack={pop}
      className="email-thread"
      bodyClassName="flex flex-col gap-3 px-3 py-4"
      footer={
        newestReceived ? (
          <div className="flex flex-wrap gap-2">
            <Button id="btn-email-reply" size="sm" onClick={() => openReply(newestReceived, "reply")}>
              <Reply className="size-4" aria-hidden="true" /> Reply
            </Button>
            <Button id="btn-email-reply-all" size="sm" variant="secondary" onClick={() => openReply(newestReceived, "all")}>
              <ReplyAll className="size-4" aria-hidden="true" /> Reply all
            </Button>
            <Button id="btn-email-forward" size="sm" variant="secondary" onClick={() => openReply(newestReceived, "forward")}>
              <Forward className="size-4" aria-hidden="true" /> Forward
            </Button>
          </div>
        ) : undefined
      }
    >
      {thread.entries.map((entry, index) => (
        <MailCard key={entry.id} entry={entry} startOpen={index === thread.entries.length - 1 || mailUnread(entry)} />
      ))}
    </SubPage>
  );
}

function openReply(email: EmailMessage, mode: "reply" | "all" | "forward") {
  const address = useData.getState().email.address ?? "";
  openCompose({ ...replyDraft(email, mode, address), forwardOf: mode === "forward" ? email : undefined } as any);
}

function MailCard({ entry, startOpen }: { entry: MailEntry; startOpen: boolean }) {
  const [open, setOpen] = useState(startOpen);
  if (entry.kind === "sent" && entry.send) {
    const send = entry.send;
    return (
      <article className="mail-card mail-sent rounded-card bg-accent-soft px-3.5 py-3" data-mail-id={entry.id}>
        <button type="button" className="flex w-full items-start justify-between gap-2 text-left" onClick={() => setOpen(!open)}>
          <div className="min-w-0">
            <div className="text-sm font-semibold">You</div>
            <div className="truncate text-[13px] text-muted">to {send.to.map(formatAddress).join(", ")}</div>
          </div>
          <div className="flex shrink-0 items-center gap-1.5 text-xs text-faint">
            <span className="mail-send-state">{sendStateLabel(entry.status?.state)}</span>
            {fmtRelative(entry.timestamp)}
          </div>
        </button>
        {entry.status?.reason && entry.status.state !== "sent" && (
          <p className="mt-1 text-[13px] text-danger">{entry.status.reason}</p>
        )}
        {open && (
          <div className="mt-2 text-[15px] whitespace-pre-wrap [overflow-wrap:anywhere]">
            <MessageText text={send.text} links />
            {!!send.attachments?.length && (
              <div className="mt-2 text-[13px] text-muted">📎 {send.attachments.map((a) => a.name).join(", ")}</div>
            )}
          </div>
        )}
      </article>
    );
  }
  const email = entry.email!;
  return (
    <article className="mail-card rounded-card bg-surface px-3.5 py-3 shadow-card" data-mail-id={entry.id}>
      <button type="button" className="flex w-full items-start justify-between gap-2 text-left" onClick={() => setOpen(!open)} aria-expanded={open}>
        <div className="min-w-0">
          <div className="mail-from truncate text-sm font-semibold">{email.from.name || email.from.address}</div>
          <div className="truncate text-[13px] text-muted">
            {email.from.name ? `${email.from.address} · ` : ""}to {(email.to ?? []).map((a) => a.name || a.address).join(", ") || "you"}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1 text-xs text-faint">
          {fmtRelative(email.date || entry.timestamp)}
          {open ? <ChevronUp className="size-4" aria-hidden="true" /> : <ChevronDown className="size-4" aria-hidden="true" />}
        </div>
      </button>
      {open && <EmailView email={email} />}
      {open && email.from.address && (
        <button
          type="button"
          className="mail-block mt-2 inline-flex items-center gap-1 text-xs text-muted"
          onClick={async () => {
            const address = email.from.address.toLowerCase();
            const ok = await askConfirm({
              title: "Block sender",
              message: <p>Mail from {address} will be dropped from now on, without telling them. You can unblock in Settings → Email.</p>,
              confirmLabel: "Block",
              confirmId: "confirm-email-block",
            });
            if (ok) await blockEmailSender(address);
          }}
        >
          <Ban className="size-3.5" aria-hidden="true" /> Block {email.from.address}
        </button>
      )}
    </article>
  );
}

/** One email's body: authentication, HTML in a sandbox or text, attachments. */
export function EmailView({ email }: { email: EmailMessage }) {
  const [loadImages, setLoadImages] = useState(false);
  const [plain, setPlain] = useState(!email.html);
  const verdict = verdictLabel(email.auth?.verdict, email.auth?.spam === "spam");
  const files = (email.attachments ?? []).filter((a) => !a.inline || !email.html);
  return (
    <div className="email-view mt-2">
      <div className="mb-2 flex flex-wrap items-center gap-1.5">
        <Chip tone={verdict.tone} className="email-verdict" title={`SPF ${email.auth?.spf ?? "?"} · DKIM ${email.auth?.dkim ?? "?"} · DMARC ${email.auth?.dmarc ?? "?"}`}>
          {verdict.tone !== "success" && <ShieldAlert className="size-3.5" aria-hidden="true" />}
          {verdict.label}
        </Chip>
        {email.tag && <Chip tone="neutral">+{email.tag}</Chip>}
        {email.html && email.text && (
          <button type="button" className="text-xs font-medium text-accent" onClick={() => setPlain(!plain)}>
            {plain ? "Formatted" : "Plain text"}
          </button>
        )}
      </div>
      {email.auth?.verdict === "failed" && (
        <p className="mb-2 text-[13px] text-danger">This message failed its sender's authentication — it may not be from {email.from.address}.</p>
      )}
      {plain || !email.html ? (
        <div className="email-text text-[15px] leading-[1.45] whitespace-pre-wrap [overflow-wrap:anywhere]">
          <MessageText text={email.text ?? ""} links />
        </div>
      ) : (
        <EmailFrame html={email.html} attachments={email.attachments ?? []} loadImages={loadImages} onBlocked={() => {}} onLoadImages={() => setLoadImages(true)} />
      )}
      {email.truncated && <p className="mt-2 text-[13px] text-muted">This email was shortened to fit. The original has everything.</p>}
      {!!files.length && (
        <div className="email-attachments mt-3 flex flex-col gap-1.5">
          {files.map((a, i) => (
            <AttachmentRow key={`${a.name}-${i}`} attachment={a} />
          ))}
        </div>
      )}
      {email.raw && (
        <button type="button" className="mt-2 inline-flex items-center gap-1 text-xs text-muted" onClick={() => void saveOriginal(email.raw!, email.subject ?? "")}>
          <FileText className="size-3.5" aria-hidden="true" /> Show original
        </button>
      )}
    </div>
  );
}

function AttachmentRow({ attachment }: { attachment: EmailAttachment }) {
  const available = Boolean(attachment.data || attachment.blob);
  return (
    <button
      type="button"
      disabled={!available}
      className="email-attachment flex items-center gap-2 rounded-lg bg-surface-2 px-3 py-2 text-left text-sm disabled:opacity-50"
      onClick={() => void saveAttachment(attachment)}
    >
      <Paperclip className="size-4 shrink-0 text-muted" aria-hidden="true" />
      <span className="min-w-0 flex-1 truncate">{attachment.name}</span>
      <span className="shrink-0 text-xs text-faint">{formatSize(attachment.size)}</span>
      {available ? <Download className="size-4 shrink-0 text-accent" aria-hidden="true" /> : <span className="text-xs text-faint">too large</span>}
    </button>
  );
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/**
 * The isolated frame. No allow-scripts: with scripts off, allow-same-origin
 * only lets this page measure the content's height, and the CSP inside the
 * document blocks every fetch but inline data (and https images on request).
 */
export function EmailFrame({ html, attachments, loadImages, onLoadImages }: {
  html: string; attachments: EmailAttachment[]; loadImages: boolean; onBlocked?: () => void; onLoadImages: () => void;
}) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [height, setHeight] = useState(120);
  const { srcdoc, blockedImages } = useMemo(() => prepareEmailHtml(html, attachments, { loadImages }), [html, attachments, loadImages]);
  useLayoutEffect(() => {
    const el = frame.current;
    if (!el) return;
    const measure = () => {
      const doc = el.contentDocument;
      if (doc?.body) setHeight(Math.min(Math.max(doc.documentElement.scrollHeight, 60), 20000));
    };
    el.addEventListener("load", measure);
    const timer = setInterval(measure, 500);
    setTimeout(() => clearInterval(timer), 5000);
    return () => {
      el.removeEventListener("load", measure);
      clearInterval(timer);
    };
  }, [srcdoc]);
  return (
    <div className="email-frame-wrap overflow-hidden rounded-lg border border-sep bg-white">
      {blockedImages && !loadImages && (
        <div className="flex items-center justify-between gap-2 border-b border-sep bg-surface-2 px-3 py-1.5 text-xs text-muted">
          <span className="inline-flex items-center gap-1">
            <ImageOff className="size-3.5" aria-hidden="true" /> Remote images blocked — they can tell the sender you opened this.
          </span>
          <button id="btn-email-load-images" type="button" className="font-medium text-accent" onClick={onLoadImages}>
            Load images
          </button>
        </div>
      )}
      <iframe
        ref={frame}
        title="Email content"
        className="email-frame block w-full border-0"
        sandbox={EMAIL_FRAME_SANDBOX}
        referrerPolicy="no-referrer"
        srcDoc={srcdoc}
        style={{ height }}
      />
    </div>
  );
}

// ── Compose ──────────────────────────────────────────────────────────────────

export function EmailCompose() {
  const params = useRoute((state) => state.params) as { draft?: Partial<Draft> & { forwardOf?: EmailMessage } };
  const pop = useRoute((state) => state.pop);
  const start = params.draft ?? {};
  const [to, setTo] = useState(start.to ?? "");
  const [cc, setCc] = useState(start.cc ?? "");
  const [bcc, setBcc] = useState(start.bcc ?? "");
  const [showCc, setShowCc] = useState(Boolean(start.cc));
  const [subject, setSubject] = useState(start.subject ?? "");
  const [text, setText] = useState(start.text ?? "");
  const [files, setFiles] = useState<File[]>([]);
  const [status, setStatus] = useState("");
  const [sending, setSending] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const sendPaused = useData((state) => state.email.sendPaused);
  const footer = useData((state) => state.email.address);

  // Forwarding carries the original's attachments the client can still open.
  useEffect(() => {
    const original = start.forwardOf;
    if (!original?.attachments?.length) return;
    void (async () => {
      const out: File[] = [];
      for (const a of original.attachments ?? []) {
        try {
          const bytes = await attachmentBytes(a);
          out.push(new File([Uint8Array.from(bytes).buffer], a.name, { type: a.mime }));
        } catch {
          // Expired on the bridge: forward without it.
        }
      }
      setFiles((current) => [...current, ...out]);
    })();
  }, []);

  const send = async () => {
    let parsed;
    try {
      parsed = { to: parseAddresses(to), cc: parseAddresses(cc), bcc: parseAddresses(bcc) };
    } catch (error) {
      setStatus((error as Error).message);
      return;
    }
    if (!parsed.to.length && !parsed.cc.length && !parsed.bcc.length) {
      setStatus("Add a recipient");
      return;
    }
    setSending(true);
    const sendId = await sendEmail({
      ...parsed, subject: subject.trim(), text, files,
      ...(start.in_reply_to ? { in_reply_to: start.in_reply_to } : {}),
      ...(start.references?.length ? { references: start.references } : {}),
    }, setStatus);
    setSending(false);
    if (sendId) pop();
    else setStatus("");
  };

  const total = files.reduce((sum, f) => sum + f.size, 0);
  return (
    <SubPage
      title={start.in_reply_to ? "Reply" : "New email"}
      onBack={pop}
      backLabel="Cancel"
      className="email-compose"
      bodyClassName="flex flex-col gap-3"
      footer={
        <div className="flex items-center gap-2">
          <Button id="btn-email-send" disabled={sending} onClick={() => void send()}>
            {sending ? "Sending…" : "Send"}
          </Button>
          <button type="button" className="flex size-10 items-center justify-center rounded-full text-accent active:bg-surface-2" aria-label="Attach files" onClick={() => fileInput.current?.click()}>
            <Paperclip className="size-5" aria-hidden="true" />
          </button>
          <span id="email-compose-status" role="status" className="min-w-0 flex-1 truncate text-[13px] text-muted">{status}</span>
        </div>
      }
    >
      {sendPaused && (
        <Notice id="email-send-paused" tone="warn">
          Sending email is paused for now, so this message may not go out. Your address still receives mail as usual.
        </Notice>
      )}
      <div>
        <Label htmlFor="email-to">To</Label>
        <Input id="email-to" type="text" inputMode="email" autoComplete="off" value={to} onChange={(e) => setTo(e.currentTarget.value)} placeholder="name@example.com" />
        {!showCc && (
          <button type="button" className="mt-1 text-xs font-medium text-accent" onClick={() => setShowCc(true)}>
            Cc / Bcc
          </button>
        )}
      </div>
      {showCc && (
        <>
          <div>
            <Label htmlFor="email-cc">Cc</Label>
            <Input id="email-cc" type="text" value={cc} onChange={(e) => setCc(e.currentTarget.value)} />
          </div>
          <div>
            <Label htmlFor="email-bcc">Bcc</Label>
            <Input id="email-bcc" type="text" value={bcc} onChange={(e) => setBcc(e.currentTarget.value)} />
          </div>
        </>
      )}
      <div>
        <Label htmlFor="email-subject">Subject</Label>
        <Input id="email-subject" type="text" value={subject} onChange={(e) => setSubject(e.currentTarget.value)} />
      </div>
      <div>
        <Label htmlFor="email-text">Message</Label>
        <Textarea id="email-text" rows={12} value={text} onChange={(e) => setText(e.currentTarget.value)} className="min-h-60 font-[inherit]" />
      </div>
      <input
        ref={fileInput}
        type="file"
        multiple
        className="hidden"
        onChange={(event) => {
          const picked = [...(event.currentTarget.files ?? [])];
          event.currentTarget.value = "";
          setFiles((current) => [...current, ...picked]);
        }}
      />
      {!!files.length && (
        <div className="flex flex-col gap-1.5">
          {files.map((file, i) => (
            <div key={`${file.name}-${i}`} className="flex items-center gap-2 rounded-lg bg-surface-2 px-3 py-2 text-sm">
              <Paperclip className="size-4 text-muted" aria-hidden="true" />
              <span className="min-w-0 flex-1 truncate">{file.name}</span>
              <span className="text-xs text-faint">{formatSize(file.size)}</span>
              <button type="button" aria-label={`Remove ${file.name}`} onClick={() => setFiles(files.filter((_, j) => j !== i))}>
                <X className="size-4 text-muted" aria-hidden="true" />
              </button>
            </div>
          ))}
          {total > MAX_EMAIL_ATTACHMENTS_BYTES && <p className="text-[13px] text-danger">Attachments may total {MAX_EMAIL_ATTACHMENTS_BYTES >> 20} MB.</p>}
        </div>
      )}
      <p className="text-xs text-faint">
        Sent from {footer ?? "your address"}. The first email to someone new ends with a short note about Poweur.
      </p>
    </SubPage>
  );
}
