/**
 * The Messages destination (E15-T2/T3/T13): trays — conversations, contact
 * requests, and anonymous messages for an inbox that accepts them — and
 * compose, as a thumb-reach button on a phone and a header action from 768px.
 */
import { useEffect } from "react";
import { onActivateKeys } from "../../lib/a11y";
import { Handshake, Lock, MessageCircle, Paperclip, Plus, SquarePen, Users, VenetianMask } from "lucide-react";
import { useShallow } from "zustand/react/shallow";
import { loadPolicy } from "../../actions/account";
import { acceptContact, blockContact, loadContacts, loadRequests, removeContact, requestContact } from "../../actions/contacts";
import {
  downloadAttachment,
  loadAnon,
  loadHistory,
  loadInbox,
  markConversationRead,
  openThread,
  refreshMessages,
  unreadFor,
} from "../../actions/messages";
import { resolveForActive } from "../../actions/relay";
import { MessageText } from "../../components/MessageText";
import { ProfileCard } from "../../components/ProfileCard";
import { fmtRelative } from "../../lib/format";
import { handleOf } from "../../lib/identity";
import { buildConversationRows, threadLabel } from "../../lib/threads.js";
import { anonymousAllowed, contactFor, incomingRequests, unreadAnonymous } from "../../state/badges";
import { useData, type Tray } from "../../state/data";
import { useRoute } from "../../state/route";
import { useSession } from "../../state/session";
import { Avatar } from "../../ui/Avatar";
import { Button } from "../../ui/Button";
import { Chip, CountBadge, EmptyState } from "../../ui/Display";
import { DestHeader } from "../../ui/Layout";
import { PullToRefresh } from "../../ui/PullToRefresh";
import { Tab, TabBar } from "../../ui/Tabs";

const TRAYS: { id: Tray; label: string }[] = [
  { id: "inbox", label: "Inbox" },
  { id: "requests", label: "Requests" },
  { id: "anonymous", label: "Anonymous" },
];

export function Messages() {
  const tray = useData((state) => state.tray);
  const policy = useData((state) => state.policy);
  const push = useRoute((state) => state.push);
  const identity = useSession((state) => state.identity) ?? "";
  const counts = useData(
    useShallow((state) => ({
      requests: incomingRequests(state, identity).length,
      anonymous: unreadAnonymous(state),
    })),
  );
  // An inbox that refuses anonymous messages has nothing to show in their tray.
  const anonOn = anonymousAllowed(policy);
  const trays = TRAYS.filter(({ id }) => id !== "anonymous" || anonOn);
  const shown: Tray = tray === "anonymous" && !anonOn ? "inbox" : tray;

  // Arriving here is a moment to look: the stream covers the rest.
  useEffect(() => {
    void loadHistory();
    void loadInbox();
    void loadRequests();
    void loadContacts();
    void loadPolicy().then(() => {
      if (anonymousAllowed(useData.getState().policy)) void loadAnon();
    });
  }, [tray]);

  useEffect(() => {
    if (policy.loaded && tray === "anonymous" && !anonOn) useData.setState({ tray: "inbox" });
  }, [policy.loaded, tray, anonOn]);

  return (
    <>
      <PullToRefresh onRefresh={refreshMessages}>
        <DestHeader title="Messages">
          <Button id="btn-compose-top" size="sm" className="dest-action hidden md:inline-flex" onClick={() => push("new-chat")}>
            <Plus className="size-4" aria-hidden="true" /> New message
          </Button>
        </DestHeader>
        <TabBar className="tray-bar" aria-label="Message trays">
          {trays.map(({ id, label }) => {
            const waiting = id === "inbox" ? 0 : counts[id];
            return (
              <Tab
                key={id}
                data-tray={id}
                active={shown === id}
                aria-label={waiting ? `${label}, ${waiting} waiting` : undefined}
                onClick={() => useData.setState({ tray: id })}
              >
                {label}
                {waiting > 0 && (
                  <span className="tray-badge inline-flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-danger px-[5px] text-[11px] leading-none font-bold text-white">
                    {waiting > 99 ? "99+" : waiting}
                  </span>
                )}
              </Tab>
            );
          })}
        </TabBar>
        {shown === "requests" ? <RequestsTray /> : shown === "anonymous" ? <AnonTray /> : <InboxTray />}
      </PullToRefresh>
      <button
        id="btn-compose"
        type="button"
        title="New message"
        aria-label="New message"
        onClick={() => push("new-chat")}
        className="fab bottom-above-nav fixed right-5 z-80 flex size-[54px] items-center justify-center rounded-full bg-accent text-white shadow-[0_4px_20px_rgb(88_86_214/.4)] transition-transform active:scale-[.93] md:hidden"
      >
        <SquarePen className="size-6" aria-hidden="true" />
      </button>
    </>
  );
}

function InboxTray() {
  const identity = useSession((state) => state.identity) ?? "";
  const messages = useData((state) => state.messages);
  const history = useData((state) => state.history);
  const contacts = useData((state) => state.contacts.list);

  const rows = buildConversationRows(messages, identity, (peer: string) => unreadFor({ messages, history }, identity, peer)).map(
    (row: any) => {
      const known = contactFor(contacts, row.contact);
      // Someone we have no entry for: adding them is one tap from the message.
      return { ...row, petname: known?.petname ?? null, stranger: !known };
    },
  );

  if (!rows.length) {
    return <EmptyState icon={MessageCircle} title="No messages yet" body="Start a conversation with New message." />;
  }

  return (
    <div className="conv-list bg-surface">
      {rows.map((row: any) => {
        const open = () => openThread(row.contact, { thread: row.threadId, group: row.group });
        return (
          <div
            key={`${row.contact}#${row.threadId}`}
            role="button"
            tabIndex={0}
            data-compose-to={row.contact}
            data-thread={row.threadId}
            data-group={row.group ? "true" : "false"}
            onClick={open}
            onKeyDown={onActivateKeys(open)}
            className="conv-row flex min-h-13 cursor-pointer items-center gap-3 border-b border-sep px-4 py-3 transition-colors last:border-b-0 focus-visible:-outline-offset-2 active:bg-surface-2 [@media(hover:hover)]:hover:bg-surface-2"
          >
            {row.group ? (
              <div className="flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-2 text-muted">
                <Users className="size-[18px]" aria-hidden="true" />
              </div>
            ) : (
              <Avatar identity={row.contact} size="md" />
            )}
            <div className="conv-info min-w-0 flex-1">
              <div className="conv-name text-base font-semibold">
                {row.petname || handleOf(row.contact)}
                {row.group && (
                  <Chip tone="success" className="ml-1.5">
                    Group
                  </Chip>
                )}
                {row.threaded && <span className="conv-thread ml-1.5 text-[13px] font-medium text-faint">#{threadLabel(row.threadId)}</span>}
              </div>
              <div className="conv-preview mt-px truncate text-sm text-muted">
                <MessageText text={row.preview} />
              </div>
            </div>
            <div className="conv-meta flex shrink-0 flex-col items-end gap-[5px]">
              <span className="conv-time text-xs text-faint">{fmtRelative(row.lastMsg.timestamp)}</span>
              <CountBadge count={row.unread} />
              {row.lastMsg.type === "chat.attachment" && row.lastMsg.metadata && (
                <Button
                  size="sm"
                  variant="secondary"
                  data-download-attachment={JSON.stringify(row.lastMsg.metadata)}
                  onClick={(event) => {
                    event.stopPropagation();
                    void downloadAttachment(row.lastMsg.metadata);
                  }}
                >
                  <Paperclip className="size-4" aria-hidden="true" /> Open
                </Button>
              )}
              {row.stranger && (
                <Button
                  size="sm"
                  variant="secondary"
                  title="Add contact"
                  data-add-contact={row.contact}
                  className="conv-add min-h-8 px-2.5 py-1 text-[13px] text-accent"
                  onClick={(event) => {
                    event.stopPropagation();
                    void requestContact(row.contact);
                  }}
                >
                  <Plus className="size-3" aria-hidden="true" /> Add
                </Button>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function TrayError({ error }: { error: string | null }) {
  return error ? <p className="val-warn p-4 text-[13px] text-warning">{error}</p> : null;
}

const sectionLabel = "tray-section-label px-4 pt-3.5 pb-1.5 text-[13px] font-semibold tracking-[.04em] text-muted uppercase";
const requestRow = "request-row flex flex-wrap items-center gap-2 border-b border-sep bg-surface pr-3 pb-3 last:border-b-0";

function RequestsTray() {
  const identity = useSession((state) => state.identity) ?? "";
  const data = useData(
    useShallow((state) => ({
      messages: state.messages,
      anon: state.anon,
      history: state.history,
      requests: state.requests,
      contacts: state.contacts,
    })),
  );
  const incoming = incomingRequests(data, identity);
  const outgoing = data.contacts.list.filter((contact) => contact.state === "requested");

  if (!incoming.length && !outgoing.length) {
    return (
      <>
        <TrayError error={data.requests.error} />
        <EmptyState icon={Handshake} title="No contact requests" body="Requests to connect land here. Accepting one lets you message each other." />
      </>
    );
  }

  return (
    <>
      <TrayError error={data.requests.error} />
      {incoming.length > 0 && (
        <>
          <div className={sectionLabel}>Waiting for you</div>
          <div className="conv-list bg-surface">
            {incoming.map((request) => (
              <div key={request.sender} className={requestRow}>
                <div className="min-w-0 flex-1">
                  <ProfileCard identity={request.sender} resolve={resolveForActive} compact className="border-b-0" />
                </div>
                {request.intro && (
                  <p className="request-intro mr-3 mb-2 ml-4 basis-full text-sm text-muted [overflow-wrap:anywhere]">{request.intro}</p>
                )}
                <div className="request-actions ml-4 flex shrink-0 items-center gap-2">
                  <Button size="sm" data-accept-contact={request.sender} onClick={() => void acceptContact(request.sender)}>
                    Accept
                  </Button>
                  <Button size="sm" variant="secondary" data-block-contact={request.sender} onClick={() => void blockContact(request.sender)}>
                    Block
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
      {outgoing.length > 0 && (
        <>
          <div className={sectionLabel}>Sent by you</div>
          <div className="conv-list bg-surface">
            {outgoing.map((contact) => (
              <div key={contact.identity} className={requestRow}>
                <div className="min-w-0 flex-1">
                  <ProfileCard identity={contact.identity} resolve={resolveForActive} compact className="border-b-0" />
                </div>
                <div className="request-actions ml-4 flex shrink-0 items-center gap-2">
                  <Chip tone="warning">Requested</Chip>
                  <Button
                    size="sm"
                    variant="secondary"
                    data-cancel-request={contact.identity}
                    onClick={() => void removeContact(contact.identity)}
                  >
                    Cancel
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </>
  );
}

/**
 * Anonymous messages are unauthenticated — encrypted to us, signed by nobody
 * — so they render as a different kind of object: no sender, no avatar, no
 * reply. That difference is the spec, not styling.
 */
function AnonTray() {
  const anon = useData((state) => state.anon);
  const unread = useData((state) => unreadAnonymous(state));

  // Looking at this tray is reading it: there is nothing to open.
  useEffect(() => {
    if (unread > 0) markConversationRead("anonymous").catch(() => {});
  }, [unread]);

  if (!anon.messages.length) {
    return (
      <>
        <TrayError error={anon.error} />
        <EmptyState
          icon={VenetianMask}
          title="No anonymous messages"
          body="Messages from strangers with no identity land here. Nobody is identified, so there is nothing to reply to."
        />
      </>
    );
  }

  return (
    <>
      <p className="anon-explainer m-0 border-b border-sep bg-warning/8 px-4 py-3 text-[13px] text-muted">
        Encrypted to you, signed by nobody. Anyone could have sent these, and there is no way to reply.
      </p>
      <div className="conv-list bg-surface">
        {anon.messages.map((message: any) => (
          <div key={message.id ?? `${message.timestamp}`} className="anon-row border-b border-sep bg-surface px-4 py-3 last:border-b-0">
            <div className="anon-row-head mb-1.5 flex items-center justify-between">
              <Chip tone="warning">Anonymous</Chip>
              <span className="conv-time text-xs text-faint">{fmtRelative(message.timestamp)}</span>
            </div>
            <div className="anon-body text-[15px] whitespace-pre-wrap text-fg [overflow-wrap:anywhere]">
              {message.plaintext ?? (
                <span className="inline-flex items-center gap-1.5 text-muted">
                  <Lock className="size-4" aria-hidden="true" /> Could not decrypt
                </span>
              )}
            </div>
          </div>
        ))}
      </div>
    </>
  );
}
