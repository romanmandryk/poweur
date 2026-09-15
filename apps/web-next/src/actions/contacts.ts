/**
 * Contacts and the requests queue (EPIC-007), from app.js. Messages needs these
 * for the Requests tray and for marking strangers; the Contacts destination
 * (E21-T8) builds on the same actions.
 */
import { confirmKeyChange } from "../components/KeyMismatchDialog";
import { contactFor } from "../state/badges";
import { useData, type Contact, type DataFields } from "../state/data";
import { useSession } from "../state/session";
import { setLoading, toast } from "../state/ui";
import { activeClient, challengeSerial, errorMessage, mergeInto, parseMessage } from "./relay";

const setContacts = (patch: Partial<DataFields["contacts"]>) =>
  useData.setState((state) => ({ contacts: { ...state.contacts, ...patch } }));
const setRequests = (patch: Partial<DataFields["requests"]>) =>
  useData.setState((state) => ({ requests: { ...state.requests, ...patch } }));

export async function loadContacts({ force = false } = {}) {
  const current = useData.getState().contacts;
  if (current.loading || (current.loaded && !force)) return;
  const client = activeClient();
  if (!client) return;

  setContacts({ loading: true, error: null });
  try {
    const contacts = await client.contacts();
    const file = await contacts.load();
    const list: Contact[] = (file.contacts ?? []).map((entry: any) => ({
      identity: entry.identity,
      petname: entry.petname ?? null,
      state: entry.state ?? "accepted",
      pinnedKey: entry.pinned_key ?? null,
    }));
    setContacts({ list, loaded: true });
  } catch (error) {
    setContacts({ error: `Could not read contacts: ${errorMessage(error)}` });
  } finally {
    setContacts({ loading: false });
  }
}

/** A drain is about *new* items, so "loaded once" is no reason to skip — only a short floor. */
const REQUEST_DRAIN_INTERVAL_MS = 2000;
let requestsInFlight: Promise<void> | null = null;
let requestsPending = false;

/** Drain the relay's contact-request queue (serialized with every other challenge read). */
export function loadRequests({ force = false } = {}): Promise<void> {
  if (requestsInFlight) {
    // A push that lands mid-drain must not be dropped: the in-flight GET may
    // have left the relay before the item was queued.
    if (force) requestsPending = true;
    return requestsInFlight;
  }
  const { fetchedAt } = useData.getState().requests;
  if (!force && fetchedAt && Date.now() - fetchedAt < REQUEST_DRAIN_INTERVAL_MS) return Promise.resolve();
  const client = activeClient();
  if (!client) return Promise.resolve();

  setRequests({ loading: true });
  requestsInFlight = challengeSerial(async () => {
    try {
      const incoming = await client.requests();
      setRequests({ incoming: mergeInto(useData.getState().requests.incoming, incoming), loaded: true, error: null });
      processContactAccepts().catch((error) => console.warn("Accept processing failed:", errorMessage(error)));
    } catch (error) {
      setRequests({ error: `Could not read requests: ${errorMessage(error)}` });
    } finally {
      setRequests({ loading: false, fetchedAt: Date.now() });
      requestsInFlight = null;
      const again = requestsPending;
      requestsPending = false;
      // Outside this serial task: chaining a drain from here would wait on a
      // promise that cannot resolve until this one does.
      if (again) queueMicrotask(() => void loadRequests({ force: true }));
    }
  });
  return requestsInFlight;
}

/** Refresh everything a contact write invalidates. */
export async function refreshContacts() {
  setContacts({ loaded: false });
  await loadContacts({ force: true });
  if (useData.getState().tray === "requests") await loadRequests({ force: true });
}

/**
 * Complete the handshake when someone answers our request: promote only
 * people we asked, and only while the key we pinned is still theirs.
 */
export async function processContactAccepts() {
  const client = activeClient();
  if (!client) return;
  await loadContacts();

  const self = useSession.getState().identity;
  const { messages, requests, contacts } = useData.getState();
  const senders = new Set(
    [...messages.map(parseMessage), ...requests.incoming]
      .filter((entry: any) => entry.type === "sys.contact.accept" && entry.sender !== self)
      .map((entry: any) => entry.sender as string)
      .filter((sender) => contactFor(contacts.list, sender)?.state === "requested"),
  );
  if (!senders.size) return;

  const api = await client.contacts();
  let promoted = 0;
  for (const sender of senders) {
    const pin = await api.checkPin(sender).catch(() => null);
    if (pin && pin.status !== "ok" && pin.status !== "unpinned") {
      toast(`${sender} accepted, but their key changed — review it in Contacts`, "warning", 8000);
      continue;
    }
    await api.set(sender, "accepted", {});
    promoted += 1;
  }
  if (promoted) await refreshContacts();
}

/** Drop a handshake just answered so it does not linger after Accept/Block. */
export function dropIncomingRequest(identity: string) {
  const wanted = String(identity ?? "").toLowerCase();
  useData.setState((state) => ({
    requests: {
      ...state.requests,
      incoming: state.requests.incoming.filter((entry: any) => String(entry.sender ?? "").toLowerCase() !== wanted),
    },
    messages: state.messages.filter((raw) => {
      const message = parseMessage(raw);
      return !(String(message.sender ?? "").toLowerCase() === wanted && message.type === "sys.contact.request");
    }),
  }));
}

async function withClient(label: string, work: (client: any) => Promise<void>): Promise<boolean> {
  const client = activeClient();
  if (!client) {
    toast("Unlock your identity first", "warning");
    return false;
  }
  setLoading(true, label);
  try {
    await work(client);
    return true;
  } catch (error) {
    toast(errorMessage(error), "error");
    return false;
  } finally {
    setLoading(false);
  }
}

export function requestContact(identity: string, { intro, petname }: { intro?: string; petname?: string } = {}) {
  return withClient(`Requesting ${identity}…`, async (client) => {
    await client.requestContact(identity, { ...(intro ? { intro } : {}), ...(petname ? { petname } : {}) });
    toast(`Contact request sent to ${identity}`, "success");
    await refreshContacts();
  });
}

/** Accept (or, `silent`, unblock): pin their key now; tell them unless unblocking. */
export function acceptContact(identity: string, { silent = false, petname }: { silent?: boolean; petname?: string } = {}) {
  return withClient(`Accepting ${identity}…`, async (client) => {
    if (silent) {
      const contacts = await client.contacts();
      await contacts.set(identity, "accepted", petname ? { petname } : {});
      toast(`${identity} unblocked`, "success");
    } else {
      const { notified } = await client.acceptContact(identity, petname ? { petname } : {});
      toast(
        notified ? `${identity} is now a contact` : `${identity} is now a contact — they could not be notified`,
        notified ? "success" : "warning",
      );
    }
    dropIncomingRequest(identity);
    await refreshContacts();
  });
}

export function blockContact(identity: string) {
  return withClient(`Blocking ${identity}…`, async (client) => {
    await client.blockContact(identity);
    toast(`${identity} blocked`, "success");
    dropIncomingRequest(identity);
    await refreshContacts();
  });
}

export function removeContact(identity: string) {
  return withClient(`Removing ${identity}…`, async (client) => {
    const contacts = await client.contacts();
    await contacts.remove(identity);
    toast(`Removed ${identity}`, "success");
    await refreshContacts();
  });
}

/**
 * Gate a send on the recipient's pin. False only when the user saw a mismatch
 * and declined; every other failure fails open — pinning is defence in depth,
 * not the security boundary.
 */
export async function checkPinBeforeSend(client: any, recipient: string): Promise<boolean> {
  let contacts: any;
  let pin: any;
  try {
    contacts = await client.contacts();
    pin = await contacts.checkPin(recipient);
  } catch {
    return true;
  }
  if (pin.status === "ok" || pin.status === "unpinned") return true;

  if (pin.status === "rotated" && pin.resolvedKey) {
    // Covered by a signed rotation (E01-T5): re-pin and say so, don't block.
    await contacts.repin(recipient, pin.resolvedKey).catch(() => {});
    toast(`${recipient} rotated their key — re-pinned`, "info");
    await refreshContacts();
    return true;
  }

  const trusted = await confirmKeyChange({ recipient, pinnedKey: pin.pinnedKey, resolvedKey: pin.resolvedKey });
  if (!trusted) return false;
  if (pin.resolvedKey) {
    await contacts.repin(recipient, pin.resolvedKey).catch(() => {});
    await refreshContacts();
  }
  return true;
}

/** Test seam. */
export function resetContactsForTests() {
  requestsInFlight = null;
  requestsPending = false;
}
