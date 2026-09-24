/**
 * Messaging (EPIC-009 web surface), ported from app.js: drain the inbox and
 * archive in one step, restore the archive on unlock, hold the push stream,
 * keep read marks, and send — signed, to a group, or with an attachment —
 * queueing offline sends for retry.
 *
 * `GET /inbox`, `/requests` and `/anon` all **drain**: the relay hands each
 * item over once and forgets it, so everything fetched is merged into the
 * store and the archive is what a reload restores from.
 */
import { ACK_STATE_READ, crypto as poweurCrypto, isSessionValid, sendsReadReceiptsTo, streamForever } from "@poweur/client";
import { isRetryableSendError, queueWebMessage, retryWebOutbox } from "../lib/outbox.js";
import { markCovers } from "../lib/threads.js";
import { loadSessionRecord } from "../lib/storage.js";
import { useData, type DataFields } from "../state/data";
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { toast } from "../state/ui";
import { trackAction } from "../lib/observability";
import { loadPolicy, loadProfile } from "./account";
import { checkPinBeforeSend, loadContacts, loadRequests, processContactAccepts } from "./contacts";
import { loadGrants, processShareRevocations } from "./files";
import { activeClient, challengeSerial, errorMessage, mergeInto, messageKey, parseMessage } from "./relay";

const selfIdentity = () => useSession.getState().identity ?? "";

export function mergeMessages(incoming: any[] | null | undefined) {
  useData.setState((state) => ({
    messages: mergeInto(state.messages.map(parseMessage), (incoming ?? []).map(parseMessage), messageKey),
  }));
}

/** An archived record in the shape the trays render. */
export function recordToMessage(record: any) {
  return {
    id: record.id,
    sender: record.sender || "",
    recipient: record.recipient,
    timestamp: record.timestamp,
    type: record.type ?? "",
    // Carried so a reload regroups into the same threads the live inbox showed.
    thread_id: record.thread_id ?? "",
    expires_at: record.expires_at ?? "",
    metadata: record.metadata,
    queue: record.queue,
    plaintext: record.body,
  };
}

/** The reverse, for archiving something the app produced itself. */
export function messageToRecord(message: any, queue: string) {
  return {
    id: message.id,
    ...(message.sender ? { sender: message.sender } : {}),
    recipient: message.recipient || selfIdentity(),
    timestamp: message.timestamp,
    ...(message.type ? { type: message.type } : {}),
    ...(message.thread_id ? { thread_id: message.thread_id } : {}),
    ...(message.expires_at ? { expires_at: message.expires_at } : {}),
    ...(message.metadata ? { metadata: message.metadata } : {}),
    queue,
    body: message.plaintext ?? "",
  };
}

/**
 * A group label on a forwarded message is only a hint until the signed roster
 * confirms the sender is a member at that epoch.
 */
async function verifyGroupInboxMessages(client: any, messages: any[]) {
  const byGroup = new Map<string, any[]>();
  for (const message of messages ?? []) {
    const group = String(message.metadata?.group ?? "").toLowerCase();
    if (!group) continue;
    if (!byGroup.has(group)) byGroup.set(group, []);
    byGroup.get(group)!.push(message);
  }
  for (const [group, candidates] of byGroup) {
    try {
      const { document } = await client.groups.roster(client.signer, group);
      const members = new Set((document.members ?? []).map((member: string) => member.toLowerCase()));
      for (const message of candidates) {
        message.group_verified =
          members.has(String(message.sender).toLowerCase()) && String(message.metadata?.epoch) === String(document.epoch ?? 0);
      }
    } catch {
      for (const message of candidates) message.group_verified = false;
    }
  }
}

let inboxInFlight: Promise<void> | null = null;
let inboxPending = false;

/**
 * Drain the inbox into the store. `force` is for callers that *know* something
 * is waiting (a push, an unlock): joining an in-flight drain would miss a
 * message posted after its GET left, so it schedules one more.
 */
export function loadInbox({ force = false } = {}): Promise<void> {
  const client = activeClient();
  if (!client) return Promise.resolve();
  if (inboxInFlight) {
    if (force) inboxPending = true;
    return inboxInFlight;
  }
  inboxInFlight = challengeSerial(async () => {
    try {
      // Read, receipt and *keep* in one step: a drain gives no second chance.
      const { messages, acks, lost } = await client.inboxAndArchive();
      if (lost) toast(`${lost} message${lost === 1 ? "" : "s"} could not be saved to your history`, "warning", 8000);
      await verifyGroupInboxMessages(client, messages);
      mergeMessages(messages);
      processShareRevocations(messages).catch((error) => console.warn("Share revocation processing failed:", errorMessage(error)));
      useData.setState((state) => ({ acks: mergeInto(state.acks, acks) }));
      processContactAccepts().catch((error) => console.warn("Accept processing failed:", errorMessage(error)));
    } catch (error) {
      console.warn("Inbox error:", errorMessage(error));
    } finally {
      inboxInFlight = null;
      const again = inboxPending;
      inboxPending = false;
      if (again) queueMicrotask(() => void loadInbox({ force: true }));
    }
  });
  return inboxInFlight;
}

let historyInFlight: Promise<void> | null = null;

const setHistory = (patch: Partial<DataFields["history"]>) =>
  useData.setState((state) => ({ history: { ...state.history, ...patch } }));

/** Restore the archive: after a reload it is the only place the messages still exist. */
export function loadHistory({ force = false } = {}): Promise<void> {
  const current = useData.getState().history;
  if (current.loading) return historyInFlight ?? Promise.resolve();
  if (current.loaded && !force) return Promise.resolve();
  const client = activeClient();
  if (!client) return Promise.resolve();

  setHistory({ loading: true });
  historyInFlight = (async () => {
    try {
      const store = await client.history();
      const [records, readState] = await Promise.all([store.load(), store.readState()]);
      // Signed conversations and the anonymous queue are different objects:
      // one has someone to reply to and one does not.
      const signed = records.filter((record: any) => record.queue !== "anonymous");
      const anonymous = records.filter((record: any) => record.queue === "anonymous");
      mergeMessages(signed.map(recordToMessage));
      useData.setState((state) => ({
        anon: { ...state.anon, messages: mergeInto(state.anon.messages, anonymous.map(recordToMessage), messageKey) },
      }));
      setHistory({ readState, loaded: true, error: null });
    } catch (error) {
      setHistory({ error: `Could not load your message history: ${errorMessage(error)}` });
      console.warn("History load failed:", errorMessage(error));
    } finally {
      setHistory({ loading: false });
      historyInFlight = null;
    }
  })();
  return historyInFlight;
}

const ANON_DRAIN_INTERVAL_MS = 2000;
let anonInFlight: Promise<void> | null = null;
let anonPending = false;

export function loadAnon({ force = false } = {}): Promise<void> {
  if (anonInFlight) {
    if (force) anonPending = true;
    return anonInFlight;
  }
  const { fetchedAt } = useData.getState().anon;
  if (!force && fetchedAt && Date.now() - fetchedAt < ANON_DRAIN_INTERVAL_MS) return Promise.resolve();
  const client = activeClient();
  if (!client) return Promise.resolve();

  const setAnon = (patch: Partial<DataFields["anon"]>) => useData.setState((state) => ({ anon: { ...state.anon, ...patch } }));
  setAnon({ loading: true });
  anonInFlight = challengeSerial(async () => {
    try {
      const { messages, lost } = await client.anonAndArchive();
      setAnon({ messages: mergeInto(useData.getState().anon.messages, messages, messageKey), loaded: true, error: null });
      if (lost) toast("Anonymous messages could not be saved to your history", "warning", 6000);
    } catch (error) {
      setAnon({ error: `Could not read anonymous messages: ${errorMessage(error)}` });
    } finally {
      setAnon({ loading: false, fetchedAt: Date.now() });
      anonInFlight = null;
      const again = anonPending;
      anonPending = false;
      if (again) queueMicrotask(() => void loadAnon({ force: true }));
    }
  });
  return anonInFlight;
}

/** Poll the anonymous queue only for the few who turned anonymous on. */
function wantsAnon() {
  const { tray, policy } = useData.getState();
  return tray === "anonymous" || Boolean(policy.doc?.anonymous?.allow);
}

let streamAbort: AbortController | null = null;

/**
 * Hold the push stream open while unlocked (E09-T2). It carries no message,
 * only "there is something" — so a reconnect costs one read, not a message.
 */
export function startEventStream() {
  const client = activeClient();
  if (!client || streamAbort) return;
  const identity = selfIdentity();
  streamAbort = new AbortController();
  streamForever(client.relay, client.signer, {
    signal: streamAbort.signal,
    onEvent: (event: { type: string }) => {
      if (identity !== selfIdentity()) return; // switched identities
      // `ready` is the catch-up cue: the stream carries no backlog.
      if (event.type === "ready") {
        void retryBrowserOutbox();
        void loadInbox({ force: true });
        void loadRequests({ force: true });
        if (wantsAnon()) void loadAnon({ force: true });
        return;
      }
      // Each queue has its own event, because each is read by a different call.
      if (event.type === "request") void loadRequests({ force: true });
      else if (event.type === "anon") void loadAnon({ force: true });
      else if (event.type === "file_request") {
        void loadGrants({ force: true });
        toast("A file request received a new upload.", "success");
      }
      else void loadInbox({ force: true });
    },
    onError: (error: Error) => console.warn("Push stream dropped, retrying:", error.message),
  } as any).catch(() => {});
}

export function stopEventStream() {
  streamAbort?.abort();
  streamAbort = null;
}

/**
 * Everything waiting, fetched the moment the identity unlocks — on whichever
 * screen that returns to, and without waiting for the stream's `ready`.
 */
export function pullAfterUnlock() {
  if (!selfIdentity() || !useSession.getState().unlocked) return;
  void loadHistory();
  void loadInbox({ force: true });
  void loadRequests({ force: true });
  void loadContacts();
  void loadProfile();
  void loadPolicy().then(() => {
    if (useData.getState().policy.doc?.anonymous?.allow) void loadAnon({ force: true });
  });
  void retryBrowserOutbox().catch(() => {});
  startEventStream();
}

/** Pull-to-refresh on Messages: every queue the screen shows, read again now. */
export function refreshMessages(): Promise<void> {
  const reads = [loadInbox({ force: true }), loadHistory({ force: true }), loadRequests({ force: true }), loadContacts({ force: true })];
  if (wantsAnon()) reads.push(loadAnon({ force: true }));
  return Promise.allSettled(reads).then(() => {});
}

/** Unread in one conversation, from its read mark — a count that can reach zero. */
export function unreadFor(data: Pick<DataFields, "messages" | "history">, identity: string, peer: string): number {
  const wanted = String(peer ?? "").toLowerCase();
  const self = String(identity).toLowerCase();
  const mark = (data.history.readState?.conversations as Record<string, any>)?.[wanted] ?? null;
  let count = 0;
  for (const raw of data.messages) {
    const message = parseMessage(raw);
    if (!message.sender || message.sender.toLowerCase() === self) continue;
    const conversation = String(message.group_verified ? message.metadata?.group : message.sender).toLowerCase();
    if (conversation !== wanted) continue;
    if (mark && markCovers(mark, message.timestamp, message.id ?? "")) continue;
    count += 1;
  }
  return count;
}

/**
 * Record that a conversation was read — the badge clears when the user
 * expects — and send read receipts (tick 3) where the policy allows.
 */
export async function markConversationRead(peer: string) {
  const client = activeClient();
  if (!client) return;
  const wanted = String(peer ?? "").toLowerCase();
  const { messages, anon } = useData.getState();
  const records = (wanted === "anonymous" ? anon.messages : messages)
    .map(parseMessage)
    .filter((message: any) => (wanted === "anonymous" ? true : message.sender && message.sender.toLowerCase() === wanted))
    .map((message: any) => messageToRecord(message, wanted === "anonymous" ? "anonymous" : "inbox"));
  if (!records.length) return;
  try {
    const store = await client.history();
    const readState = await store.markConversationRead(wanted, records);
    setHistory({ readState });
    let policy = useData.getState().policy.doc;
    if (!policy) ({ policy } = await client.policy());
    if (wanted !== "anonymous" && sendsReadReceiptsTo(policy, wanted)) {
      const inbound = useData
        .getState()
        .messages.map(parseMessage)
        .filter((message: any) => message.sender?.toLowerCase() === wanted && message.id && message.plaintext != null);
      await Promise.allSettled(inbound.map((message: any) => client.messages.ack(client.signer, message, { state: ACK_STATE_READ })));
    }
  } catch (error) {
    console.warn("Could not save read marks:", errorMessage(error));
  }
}

export type SendOutcome = { status: "sent" | "queued" | "blocked" | "failed"; delivered?: number; total?: number };
export type SetStatus = (text: string, tone?: "" | "ok" | "err") => void;

/**
 * Send signed — direct or to a group — keeping our own copy in the store (the
 * relay never hands a sender their message back).
 */
export async function sendSigned(
  client: any,
  {
    to,
    body,
    attachment = null,
    thread = "",
    group = false,
    setStatus,
  }: { to: string; body: string; attachment?: File | null; thread?: string; group?: boolean; setStatus: SetStatus },
): Promise<SendOutcome> {
  const self = selfIdentity();
  if (group) {
    try {
      setStatus("Reading the signed group roster…");
      const sent = await client.sendGroupAndArchive(to, body, { ...(thread ? { thread } : {}) });
      const first = sent.envelopes[0];
      mergeMessages([
        {
          id: first.id,
          sender: self,
          recipient: sent.group.group,
          timestamp: first.timestamp,
          queue: "sent",
          plaintext: body,
          thread_id: first.thread_id,
          metadata: first.metadata,
          group_verified: true,
        },
      ]);
      const delivered = sent.response.delivered?.length ?? 0;
      const total = sent.envelopes.length;
      setStatus(`✓ ${delivered} of ${total} delivered`, delivered === total ? "ok" : "err");
      if (sent.lost) toast("Sent, but not saved to your history", "warning", 6000);
      trackAction("send", { kind: "group", outcome: "sent" });
      return { status: "sent", delivered, total };
    } catch (error) {
      setStatus(`✕ ${errorMessage(error)}`, "err");
      toast(errorMessage(error), "error");
      trackAction("send", { kind: "group", outcome: "failed" });
      return { status: "failed" };
    }
  }

  try {
    setStatus("Checking their key…");
    if (!(await checkPinBeforeSend(client, to))) {
      setStatus("✕ Not sent — key not trusted", "err");
      trackAction("send", { kind: "chat", outcome: "blocked" });
      return { status: "blocked" };
    }
    setStatus("Sending…");
    const sent = attachment
      ? await client.sendAttachment(to, new Uint8Array(await attachment.arrayBuffer()), {
          name: attachment.name,
          mime: attachment.type || "application/octet-stream",
          caption: body || attachment.name,
          ...(thread ? { threadId: thread } : {}),
        })
      : await client.sendAndArchive(to, body, {
          signWith: isSessionValid(loadSessionRecord(self)) ? "session" : "identity",
          ...(thread ? { threadId: thread } : {}),
        });
    setStatus("✓ Sent", "ok");
    mergeMessages([
      {
        id: sent.message.id,
        sender: self,
        recipient: sent.message.recipient,
        timestamp: sent.message.timestamp,
        queue: "sent",
        plaintext: body || attachment?.name,
        ...(attachment ? { type: "chat.attachment", metadata: sent.message.metadata } : {}),
        ...(thread ? { thread_id: thread } : {}),
      },
    ]);
    if (sent.lost) toast("Sent, but not saved to your history", "warning", 6000);
    trackAction("send", { kind: attachment ? "attachment" : "chat", outcome: "sent" });
    return { status: "sent" };
  } catch (error) {
    if (!attachment && client.decryptor && isRetryableSendError(error)) {
      const sealed = poweurCrypto.encryptMessage(client.decryptor.encryptionPublicKey, body);
      queueWebMessage(self, to, sealed, { signWith: "identity", ...(thread ? { threadId: thread } : {}) }, errorMessage(error));
      setStatus("· Queued — will retry when online", "ok");
      toast("Message queued until the relay is reachable", "success");
      trackAction("send", { kind: "chat", outcome: "queued" });
      return { status: "queued" };
    }
    setStatus(`✕ ${errorMessage(error)}`, "err");
    toast(errorMessage(error), "error");
    trackAction("send", { kind: attachment ? "attachment" : "chat", outcome: "failed" });
    return { status: "failed" };
  }
}

export async function retryBrowserOutbox() {
  const client = activeClient();
  if (!client?.decryptor) return;
  const privateKey = await client.decryptor.privateKeyBytes();
  const result: any = await retryWebOutbox(selfIdentity(), (entry: any) => {
    const plaintext = poweurCrypto.decryptMessage(privateKey, entry.payload, entry.encryption);
    return client.sendAndArchive(entry.recipient, plaintext, entry.options);
  });
  if (result?.sent) toast(`${result.sent} queued message${result.sent === 1 ? "" : "s"} sent`, "success");
}

/** Open a conversation. Opening it is reading it. */
export function openThread(peer: string, { thread = "", group = false }: { thread?: string; group?: boolean } = {}) {
  useData.setState({ thread: { peer, threadId: thread, group } });
  markConversationRead(peer).catch(() => {});
  useRoute.getState().push("thread", { to: peer, thread, group });
  trackAction("open-thread", { kind: group ? "group" : "chat" });
}

/**
 * Open a conversation with someone picked by name. A group is recognised by
 * its roster answering — the same check sending to it would make.
 */
export async function openNewChat(identity: string): Promise<boolean> {
  const client = activeClient();
  if (!client) {
    toast("Unlock your identity first", "warning");
    return false;
  }
  let group = false;
  try {
    await client.groups.roster(client.signer, identity);
    group = true;
  } catch {
    // Not a group, or not one we belong to: a direct conversation.
  }
  // A group's main thread is the group's own address (E09-T5).
  openThread(identity, { thread: group ? identity : "", group });
  return true;
}

/** Fetch, hash-verify and hand over an attachment. */
export async function downloadAttachment(metadata: unknown) {
  const client = activeClient();
  if (!client) return;
  try {
    const { ref, bytes } = await client.downloadAttachment(metadata);
    const url = URL.createObjectURL(new Blob([bytes], { type: ref.mime }));
    if (String(ref.mime).startsWith("image/")) {
      window.open(url, "_blank", "noopener");
    } else {
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = ref.name;
      anchor.click();
    }
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  } catch (error) {
    toast(`Attachment failed: ${errorMessage(error)}`, "error");
  }
}

/** Test seam. */
export function resetMessagingForTests() {
  inboxInFlight = null;
  inboxPending = false;
  historyInFlight = null;
  anonInFlight = null;
  anonPending = false;
  stopEventStream();
}
