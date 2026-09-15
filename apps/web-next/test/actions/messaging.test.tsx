import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";

const holder = vi.hoisted(() => ({ client: null as any }));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => holder.client,
}));

import { acceptContact, blockContact, loadRequests, resetContactsForTests } from "../../src/actions/contacts";
import {
  loadHistory,
  loadInbox,
  markConversationRead,
  resetMessagingForTests,
  sendSigned,
  unreadFor,
} from "../../src/actions/messages";
import { mergeInto, messageKey } from "../../src/actions/relay";
import { loadWebOutbox } from "../../src/lib/outbox.js";
import { PanelHost } from "../../src/shell/Overlays";
import { useData } from "../../src/state/data";
import { useSession } from "../../src/state/session";
import { fakeClient, inbound } from "../helpers/fake-client";
import { resetStores } from "../helpers/stores";

const ME = "alice.poweur.net";
const BOB = "bob.poweur.net";

beforeEach(() => {
  resetStores();
  resetMessagingForTests();
  resetContactsForTests();
  holder.client = fakeClient();
  useSession.setState({ identity: ME, unlocked: true });
});

describe("merging drained items", () => {
  it("dedupes by key, later wins, and keeps oldest first", () => {
    const a = inbound(BOB, "one", 1);
    const b = inbound(BOB, "two", 2);
    const merged = mergeInto([b], [a, { ...b, plaintext: "two, edited" }], messageKey);
    expect(merged.map((m) => m.plaintext)).toEqual(["one", "two, edited"]);
  });
});

describe("loadInbox", () => {
  it("merges drained messages and acks into the store", async () => {
    holder.client.inboxAndArchive.mockResolvedValueOnce({
      messages: [inbound(BOB, "hello", 1)],
      acks: [{ id: "ack-1", message_id: "sent-1", state: "delivered", timestamp: "2026-09-15T10:00:02Z" }],
      lost: 0,
    });
    await loadInbox();
    expect(useData.getState().messages.map((m: any) => m.plaintext)).toEqual(["hello"]);
    expect(useData.getState().acks).toHaveLength(1);
  });

  it("is single-flight, and a forced call mid-drain drains once more", async () => {
    let release!: () => void;
    holder.client.inboxAndArchive.mockImplementationOnce(
      () => new Promise((resolve) => (release = () => resolve({ messages: [], acks: [], lost: 0 }))),
    );
    const first = loadInbox();
    await Promise.resolve();
    expect(loadInbox({ force: true })).toBe(first);
    release();
    await first;
    await waitFor(() => expect(holder.client.inboxAndArchive).toHaveBeenCalledTimes(2));
  });
});

describe("loadHistory", () => {
  it("restores signed conversations and the anonymous queue into separate trays", async () => {
    holder.client.store.load.mockResolvedValueOnce([
      { id: "h1", sender: BOB, recipient: ME, timestamp: "2026-09-15T09:00:00Z", queue: "inbox", body: "from the archive" },
      { id: "h2", recipient: ME, timestamp: "2026-09-15T09:01:00Z", queue: "anonymous", body: "a tip" },
    ]);
    holder.client.store.readState.mockResolvedValueOnce({ conversations: { [BOB]: { timestamp: "2026-09-15T09:00:00Z", id: "h1" } } });
    await loadHistory();
    const state = useData.getState();
    expect(state.messages.map((m: any) => m.plaintext)).toEqual(["from the archive"]);
    expect(state.anon.messages.map((m: any) => m.plaintext)).toEqual(["a tip"]);
    expect(state.history.loaded).toBe(true);
    expect(unreadFor(state, ME, BOB)).toBe(0);
  });
});

describe("unread and read marks", () => {
  it("counts only their messages past the mark, and marking reads sends receipts", async () => {
    useData.setState({ messages: [inbound(BOB, "one", 1), inbound(BOB, "two", 2), { ...inbound(ME, "mine", 3), recipient: BOB }] });
    expect(unreadFor(useData.getState(), ME, BOB)).toBe(2);

    await markConversationRead(BOB);
    expect(holder.client.store.markConversationRead).toHaveBeenCalledWith(BOB, expect.arrayContaining([expect.objectContaining({ body: "one" })]));
    expect(unreadFor(useData.getState(), ME, BOB)).toBe(0);
    // Default policy sends read receipts: one per inbound message.
    expect(holder.client.messages.ack).toHaveBeenCalledTimes(2);
  });
});

describe("sendSigned", () => {
  const statuses: string[] = [];
  const setStatus = (text: string) => statuses.push(text);
  beforeEach(() => {
    statuses.length = 0;
  });

  it("sends signed and keeps our own copy, which the relay never returns", async () => {
    const outcome = await sendSigned(holder.client, { to: BOB, body: "hi bob", setStatus });
    expect(outcome).toEqual({ status: "sent" });
    expect(holder.client.sendAndArchive).toHaveBeenCalledWith(BOB, "hi bob", { signWith: "identity" });
    expect(useData.getState().messages).toEqual([expect.objectContaining({ sender: ME, plaintext: "hi bob", queue: "sent" })]);
    expect(statuses).toEqual(["Checking their key…", "Sending…", "✓ Sent"]);
  });

  it("queues a send the relay could not take, for retry", async () => {
    const encryption = new Uint8Array(32).fill(9);
    holder.client.decryptor = { encryptionPublicKey: encryption };
    holder.client.sendAndArchive.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const outcome = await sendSigned(holder.client, { to: BOB, body: "while offline", setStatus });
    expect(outcome).toEqual({ status: "queued" });
    expect(loadWebOutbox(ME)).toHaveLength(1);
    expect(statuses.at(-1)).toBe("· Queued — will retry when online");
  });

  it("stops at a changed key unless the user trusts it", async () => {
    holder.client.contactsApi.checkPin.mockResolvedValue({ status: "mismatch", pinnedKey: "ed25519:AAAA", resolvedKey: "ed25519:BBBB" });
    render(<PanelHost />);

    let outcome: Promise<unknown>;
    act(() => {
      outcome = sendSigned(holder.client, { to: BOB, body: "careful", setStatus });
    });
    await waitFor(() => expect(document.querySelector("#km-cancel")).toBeTruthy());
    fireEvent.click(document.querySelector("#km-cancel")!);
    await expect(outcome!).resolves.toEqual({ status: "blocked" });
    expect(holder.client.sendAndArchive).not.toHaveBeenCalled();

    act(() => {
      outcome = sendSigned(holder.client, { to: BOB, body: "trusted", setStatus });
    });
    await waitFor(() => expect(document.querySelector("#km-trust")).toBeTruthy());
    fireEvent.click(document.querySelector("#km-trust")!);
    await expect(outcome!).resolves.toEqual({ status: "sent" });
    expect(holder.client.contactsApi.repin).toHaveBeenCalledWith(BOB, "ed25519:BBBB");
  });
});

describe("requests", () => {
  it("drains the queue, and answering one drops it", async () => {
    holder.client.requests.mockResolvedValueOnce([
      { id: "r1", sender: BOB, timestamp: "2026-09-15T10:00:00Z", type: "sys.contact.request", plaintext: "hi" },
      { id: "r2", sender: "carol.poweur.net", timestamp: "2026-09-15T10:00:01Z", type: "sys.contact.request" },
    ]);
    await loadRequests({ force: true });
    expect(useData.getState().requests.incoming).toHaveLength(2);

    await acceptContact(BOB);
    expect(holder.client.acceptContact).toHaveBeenCalledWith(BOB, {});
    await blockContact("carol.poweur.net");
    expect(holder.client.blockContact).toHaveBeenCalledWith("carol.poweur.net");
    expect(useData.getState().requests.incoming).toEqual([]);
  });

  it("finishes our own handshake when they accept", async () => {
    holder.client.contactsApi.load.mockResolvedValue({ contacts: [{ identity: BOB, state: "requested" }] });
    holder.client.requests.mockResolvedValueOnce([{ id: "a1", sender: BOB, timestamp: "2026-09-15T10:00:00Z", type: "sys.contact.accept" }]);
    await loadRequests({ force: true });
    await waitFor(() => expect(holder.client.contactsApi.set).toHaveBeenCalledWith(BOB, "accepted", {}));
  });
});
