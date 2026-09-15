/**
 * One conversation, as chat bubbles (E15-T13). Renders from the store the
 * archive filled at unlock, so "Load more" reveals what the app already holds.
 *
 * The draft, the scroll position and the page size are component state now:
 * a message arriving no longer rebuilds the view, so none of them has to be
 * put back by hand.
 */
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Check, CheckCheck, ChevronLeft, CircleAlert, Hourglass, MessageCircle, Paperclip, Users, type LucideIcon } from "lucide-react";
import { downloadAttachment, markConversationRead, sendSigned, unreadFor } from "../../actions/messages";
import { activeClient } from "../../actions/relay";
import { MessageText } from "../../components/MessageText";
import { cn } from "../../lib/cn";
import { dayLabel, fmtClock } from "../../lib/format";
import { handleOf } from "../../lib/identity";
import { bodyFor, deliveryState, expiryCountdown, latestWindow, threadLabel, threadMessages, THREAD_PAGE_SIZE } from "../../lib/threads.js";
import { contactFor } from "../../state/badges";
import { useData } from "../../state/data";
import { useRoute } from "../../state/route";
import { useSession } from "../../state/session";
import { toast } from "../../state/ui";
import { Avatar } from "../../ui/Avatar";
import { Button } from "../../ui/Button";
import { EmptyState } from "../../ui/Display";

const TICKS: Record<string, [LucideIcon, string]> = {
  sent: [Check, "Sent"],
  delivered: [CheckCheck, "Delivered"],
  read: [CheckCheck, "Read"],
  failed: [CircleAlert, "Not delivered"],
};

/** Sent is one tick, delivered two; read and failed are drawn heavier. */
function DeliveryTick({ state }: { state: string }) {
  const [Icon, label] = TICKS[state];
  return (
    <span className={cn("bubble-tick ml-1 inline-flex align-[-2px]", `tick-${state}`)} title={label} aria-label={label}>
      <Icon className="size-3.5" strokeWidth={state === "read" || state === "failed" ? 3 : 2} aria-hidden="true" />
    </span>
  );
}

/** A fresh view per conversation: page size, draft and scroll do not leak between them. */
export function ThreadScreen() {
  const thread = useData((state) => state.thread);
  if (!thread) return null;
  return <Thread key={`${thread.peer}#${thread.threadId}`} peer={thread.peer} threadId={thread.threadId} group={thread.group} />;
}

function Thread({ peer, threadId, group }: { peer: string; threadId: string; group: boolean }) {
  const identity = useSession((state) => state.identity) ?? "";
  const messages = useData((state) => state.messages);
  const acks = useData((state) => state.acks);
  const history = useData((state) => state.history);
  const contacts = useData((state) => state.contacts.list);
  const pop = useRoute((state) => state.pop);

  const [shown, setShown] = useState(THREAD_PAGE_SIZE);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [status, setStatus] = useState<{ text: string; tone: string }>({ text: "", tone: "" });
  const body = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const anchor = useRef<{ height: number; top: number } | null>(null);
  const marking = useRef(false);
  const fileInput = useRef<HTMLInputElement>(null);

  const all = threadMessages(messages, identity, peer, threadId);
  const { visible, hidden, hasMore } = latestWindow(all, shown);
  const unread = unreadFor({ messages, history }, identity, peer);
  const title = group ? handleOf(peer) : contactFor(contacts, peer)?.petname || handleOf(peer);
  const self = identity.toLowerCase();

  // Pinned to the newest message while the reader is there, held in place
  // when they scrolled up, anchored after "Load more" prepends.
  useLayoutEffect(() => {
    const element = body.current;
    if (!element) return;
    if (anchor.current) {
      element.scrollTop = element.scrollHeight - anchor.current.height + anchor.current.top;
      anchor.current = null;
    } else if (atBottom.current) {
      element.scrollTop = element.scrollHeight;
    }
  }, [all.length, shown]);

  // A message that arrives while the conversation is open has been read.
  useEffect(() => {
    if (unread === 0 || marking.current) return;
    marking.current = true;
    void markConversationRead(peer).finally(() => {
      marking.current = false;
    });
  }, [unread, peer]);

  const send = async (attachment: File | null = null) => {
    if (sending) return;
    const text = draft.trim();
    if (!text && !attachment) return;
    const client = activeClient();
    if (!client) {
      toast("Unlock your identity first", "warning");
      return;
    }
    setSending(true);
    try {
      const outcome = await sendSigned(client, {
        to: peer,
        body: text,
        attachment,
        thread: threadId,
        group,
        setStatus: (message, tone = "") => setStatus({ text: message, tone }),
      });
      if (outcome.status === "sent" || outcome.status === "queued") {
        setDraft("");
        atBottom.current = true;
      }
      // The bubble and its tick say "sent"; a partial group delivery or a
      // queued send is worth keeping on screen.
      if (outcome.status === "sent" && (!outcome.total || outcome.delivered === outcome.total)) {
        setStatus({ text: "", tone: "" });
      }
    } finally {
      setSending(false);
    }
  };

  let lastDay = "";

  return (
    <div className="sub-page thread-view flex h-dvh animate-fade-in flex-col bg-bg lg:[#detail-pane_&]:h-full">
      <div className="sub-header flex h-header shrink-0 items-center gap-3 border-b border-sep bg-chrome px-4 pt-safe backdrop-blur-xl">
        <button
          id="btn-back"
          type="button"
          aria-label="Back to messages"
          onClick={pop}
          className="btn-back -ml-1 flex items-center rounded-lg p-1 text-accent active:opacity-60"
        >
          <ChevronLeft className="size-6" strokeWidth={2.4} aria-hidden="true" />
        </button>
        {group ? (
          <div className="flex size-7 shrink-0 items-center justify-center rounded-full bg-surface-2 text-muted">
            <Users className="size-4" aria-hidden="true" />
          </div>
        ) : (
          <Avatar identity={peer} />
        )}
        <span className="sub-title thread-title flex min-w-0 flex-1 items-center gap-1.5 truncate text-[17px] font-bold">
          {title}
          {threadId && !group && <span className="conv-thread text-[13px] font-medium text-faint">#{threadLabel(threadId)}</span>}
        </span>
      </div>

      <div
        ref={body}
        id="thread-body"
        role="log"
        aria-label={`Conversation with ${title}`}
        onScroll={(event) => {
          const element = event.currentTarget;
          atBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 48;
        }}
        className="thread-body flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto overscroll-contain px-3 pt-3 pb-2"
      >
        {hasMore && (
          <div className="thread-more flex justify-center pt-1 pb-3">
            <Button
              id="btn-thread-more"
              size="sm"
              variant="secondary"
              onClick={() => {
                const element = body.current;
                if (element) anchor.current = { height: element.scrollHeight, top: element.scrollTop };
                setShown((current) => current + THREAD_PAGE_SIZE);
              }}
            >
              Load more ({hidden} earlier)
            </Button>
          </div>
        )}
        {visible.length === 0 ? (
          <EmptyState icon={MessageCircle} title="No messages yet" body="Say hello below." />
        ) : (
          visible.map((message: any, index: number) => {
            const mine = String(message.sender).toLowerCase() === self;
            const day = dayLabel(message.timestamp);
            const separator = day && day !== lastDay;
            lastDay = day;
            const countdown = expiryCountdown(message.expires_at);
            const state = mine && !group ? deliveryState(message, acks) : null;
            return (
              <div key={message.id ?? `${message.timestamp}-${index}`} className="contents">
                {separator && (
                  <div className="thread-day mt-2.5 mb-1.5 self-center rounded-full bg-surface-2 px-2.5 py-0.5 text-xs text-muted">{day}</div>
                )}
                <div className={cn("bubble-row flex", mine ? "mine justify-end" : "theirs")} data-message-id={message.id ?? ""}>
                  <div
                    className={cn(
                      "bubble max-w-[min(78%,520px)] rounded-[18px] px-3 pt-2 pb-1.5 shadow-card [overflow-wrap:anywhere]",
                      mine ? "rounded-br-md bg-accent text-white" : "rounded-bl-md bg-surface text-fg",
                    )}
                  >
                    {group && !mine && (
                      <div className="bubble-sender mb-0.5 text-xs font-semibold text-accent">
                        {contactFor(contacts, message.sender)?.petname || handleOf(message.sender)}
                      </div>
                    )}
                    <div className="bubble-text text-base leading-[1.35] whitespace-pre-wrap">
                      <MessageText text={bodyFor(message)} />
                    </div>
                    {message.type === "chat.attachment" && message.metadata && (
                      <Button
                        size="sm"
                        variant="secondary"
                        className="bubble-attachment mt-1.5"
                        data-download-attachment={JSON.stringify(message.metadata)}
                        onClick={() => void downloadAttachment(message.metadata)}
                      >
                        <Paperclip className="size-4" aria-hidden="true" /> Open
                      </Button>
                    )}
                    <div className="bubble-meta mt-0.5 text-right text-[11px] opacity-75">
                      {countdown && (
                        <>
                          <Hourglass className="mr-0.5 inline size-3 align-[-2px]" aria-hidden="true" />
                          {countdown} ·{" "}
                        </>
                      )}
                      {fmtClock(message.timestamp)}
                      {/* Per-member ticks for a group are their own design (E09-T5). */}
                      {state && <DeliveryTick state={state} />}
                    </div>
                  </div>
                </div>
              </div>
            );
          })
        )}
      </div>

      {status.text && (
        <p
          className={cn(
            "compose-status thread-status m-0 shrink-0 bg-bg px-4 pt-1.5 text-sm text-muted",
            status.tone === "ok" && "ok text-success",
            status.tone === "err" && "err text-danger",
          )}
        >
          {status.text}
        </p>
      )}

      <div className="thread-composer flex shrink-0 items-end gap-2 border-t border-sep bg-bg px-3 pt-2 pb-[calc(8px+env(safe-area-inset-bottom,0px))]">
        {!group && (
          <>
            <label
              htmlFor="thread-file"
              title="Attach a file (up to 20 MB)"
              aria-label="Attach a file"
              className="thread-attach flex size-10 shrink-0 cursor-pointer items-center justify-center rounded-full text-muted active:bg-surface-2"
            >
              <Paperclip className="size-5" aria-hidden="true" />
            </label>
            <input
              ref={fileInput}
              id="thread-file"
              type="file"
              hidden
              onChange={(event) => {
                const file = event.currentTarget.files?.[0];
                // Picking a file sends it, with whatever is typed as its caption.
                if (file) void send(file);
                if (fileInput.current) fileInput.current.value = "";
              }}
            />
          </>
        )}
        <textarea
          id="thread-input"
          rows={1}
          placeholder="Message"
          aria-label="Message"
          value={draft}
          onChange={(event) => setDraft(event.currentTarget.value)}
          onKeyDown={(event) => {
            // Enter is a newline on a phone keyboard; Cmd/Ctrl+Enter sends.
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
              event.preventDefault();
              void send();
            }
          }}
          className="thread-input max-h-35 min-h-10 min-w-0 flex-1 resize-none rounded-[20px] border border-sep bg-surface px-3.5 py-[9px] text-base text-fg focus:border-accent focus:shadow-[0_0_0_3px_var(--color-accent-soft)] focus:outline-none"
        />
        <Button id="btn-thread-send" className="w-auto shrink-0 px-5 py-2.5 text-base" disabled={sending} onClick={() => void send()}>
          Send
        </Button>
      </div>
    </div>
  );
}
