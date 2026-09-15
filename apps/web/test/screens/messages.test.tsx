import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";

const holder = vi.hoisted(() => ({ client: null as any }));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => holder.client,
}));
vi.mock("@poweur/client", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  // The push stream stays open forever; here it never says anything.
  streamForever: vi.fn(() => new Promise(() => {})),
}));

import { resetContactsForTests } from "../../src/actions/contacts";
import { resetMessagingForTests } from "../../src/actions/messages";
import { App } from "../../src/shell/App";
import { useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { useSession } from "../../src/state/session";
import { fakeClient, inbound } from "../helpers/fake-client";
import { resetStores } from "../helpers/stores";

const ME = "alice.poweur.net";
const BOB = "bob.poweur.net";
const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);
const $$ = (selector: string) => document.querySelectorAll(selector);

beforeEach(() => {
  resetStores();
  resetMessagingForTests();
  resetContactsForTests();
  holder.client = fakeClient();
  useSession.setState({ identity: ME, unlocked: true });
});

describe("Messages destination (E21-T7)", () => {
  it("unlocking pulls the inbox, requests and history, whatever the screen", async () => {
    useRoute.setState({ page: "files" });
    render(<App />);
    await waitFor(() => expect(holder.client.inboxAndArchive).toHaveBeenCalled());
    expect(holder.client.requests).toHaveBeenCalled();
    expect(holder.client.history).toHaveBeenCalled();
  });

  it("shows the inbox and requests trays, an empty inbox, and a compose button", async () => {
    render(<App />);
    expect($$(".tray-tab")).toHaveLength(2);
    expect($('.tray-tab[data-tray="anonymous"]')).toBeNull();
    expect($(".tray-tab.active")!.textContent).toBe("Inbox");
    expect($(".empty-state-title")!.textContent).toBe("No messages yet");
    fireEvent.click($('.tray-tab[data-tray="requests"]')!);
    expect($(".empty-state-title")!.textContent).toBe("No contact requests");

    fireEvent.click($("#btn-compose")!);
    expect(useRoute.getState().sub).toBe("new-chat");
  });

  it("lists a conversation per contact with its preview and unread count, and opening it reads it", async () => {
    holder.client.inboxAndArchive.mockResolvedValueOnce({
      messages: [inbound(BOB, "first", 1), inbound(BOB, "latest from bob", 2)],
      acks: [],
      lost: 0,
    });
    render(<App />);
    const row = await waitFor(() => {
      const element = $(`.conv-row[data-compose-to="${BOB}"]`);
      expect(element).toBeTruthy();
      return element!;
    });
    expect(row.querySelector(".conv-preview")!.textContent).toBe("latest from bob");
    expect(row.querySelector(".conv-badge")!.textContent).toBe("2");
    // A stranger gets an Add button right on the row.
    expect(row.querySelector(`[data-add-contact="${BOB}"]`)).toBeTruthy();
    expect($('.nav-tab[data-page="messages"]')!.getAttribute("aria-label")).toBe("Messages, 2 new");

    fireEvent.click(row);
    expect(useRoute.getState().sub).toBe("thread");
    await waitFor(() => expect(holder.client.store.markConversationRead).toHaveBeenCalledWith(BOB, expect.any(Array)));
    await waitFor(() => expect($('.nav-tab[data-page="messages"]')!.getAttribute("aria-label")).toBe("Messages"));
  });

  it("the requests tray answers a request and shows ours", async () => {
    // The queue drains: the relay hands a request over once, then it is gone.
    holder.client.requests.mockResolvedValueOnce([
      { id: "r1", sender: BOB, timestamp: "2026-09-15T10:00:00Z", type: "sys.contact.request", plaintext: "hello, it's bob" },
    ]);
    holder.client.contactsApi.load.mockResolvedValue({ contacts: [{ identity: "carol.poweur.net", state: "requested" }] });
    useData.setState({ tray: "requests" });
    render(<App />);

    await waitFor(() => expect($(`[data-accept-contact="${BOB}"]`)).toBeTruthy());
    expect($(".request-intro")!.textContent).toBe("hello, it's bob");
    expect($('.tray-tab[data-tray="requests"] .tray-badge')!.textContent).toBe("1");
    await waitFor(() => expect($('[data-cancel-request="carol.poweur.net"]')).toBeTruthy());

    fireEvent.click($(`[data-accept-contact="${BOB}"]`)!);
    await waitFor(() => expect(holder.client.acceptContact).toHaveBeenCalledWith(BOB, {}));
    await waitFor(() => expect($(`[data-accept-contact="${BOB}"]`)).toBeNull());
  });

  it("the anonymous tray shows unsigned messages as nobody's, and looking reads them", async () => {
    useData.setState({ tray: "anonymous" });
    holder.client.policy.mockResolvedValue({ policy: { version: 1, mode: "open", anonymous: { allow: true } }, explicit: true });
    holder.client.anonAndArchive.mockResolvedValue({
      messages: [{ id: "x1", timestamp: "2026-09-15T10:00:00Z", plaintext: "a tip from nobody" }],
      lost: 0,
    });
    render(<App />);
    await waitFor(() => expect($(".anon-body")?.textContent).toBe("a tip from nobody"));
    expect($(".anon-explainer")).toBeTruthy();
    expect($(".anon-row .id-avatar")).toBeNull();
    await waitFor(() => expect(holder.client.store.markConversationRead).toHaveBeenCalledWith("anonymous", expect.any(Array)));
  });

  it("offers the anonymous tray only while the inbox policy accepts anonymous messages", async () => {
    useData.setState({ tray: "anonymous" });
    holder.client.policy.mockResolvedValue({ policy: { version: 1, mode: "open" }, explicit: true });
    render(<App />);
    await waitFor(() => expect(useData.getState().tray).toBe("inbox"));
    expect($('.tray-tab[data-tray="anonymous"]')).toBeNull();
    expect($(".tray-tab.active")!.textContent).toBe("Inbox");

    act(() => useData.setState((state) => ({ policy: { ...state.policy, doc: { version: 1, mode: "open", anonymous: { allow: true } } as any } })));
    expect($$(".tray-tab")).toHaveLength(3);
  });
});

describe("New chat", () => {
  it("opens a direct conversation with someone found by name", async () => {
    useRoute.setState({ sub: "new-chat" });
    const { container } = render(<App />);
    expect($(".new-chat")).toBeTruthy();
    const input = container.querySelector<HTMLInputElement>(".new-chat .idin input")!;
    input.value = BOB;
    // Resolution goes over the network in the real app; skip straight to Enter's submit path.
    holder.client.groups.roster.mockRejectedValueOnce(new Error("not a group"));
    await act(async () => {
      const { openNewChat } = await import("../../src/actions/messages");
      await openNewChat(BOB);
    });
    expect(useRoute.getState().sub).toBe("thread");
    expect(useData.getState().thread).toEqual({ peer: BOB, threadId: "", group: false });
  });

  it("recognises a group by its roster answering", async () => {
    holder.client.groups.roster.mockResolvedValueOnce({ document: { members: [] } });
    const { openNewChat } = await import("../../src/actions/messages");
    await openNewChat("team.poweur.net");
    expect(useData.getState().thread).toEqual({ peer: "team.poweur.net", threadId: "team.poweur.net", group: true });
  });
});

describe("Conversation view (E15-T13)", () => {
  const openWith = (messages: any[]) => {
    useData.setState({ messages, thread: { peer: BOB, threadId: "", group: false } });
    useRoute.setState({ sub: "thread" });
  };

  it("shows the newest ten, then the rest on demand", async () => {
    openWith(Array.from({ length: 12 }, (_, index) => inbound(BOB, `history ${index + 1}`, index)));
    render(<App />);
    expect($$(".bubble-row")).toHaveLength(10);
    expect($(".thread-body")!.textContent).toContain("history 12");
    expect($("#btn-thread-more")!.textContent).toContain("2 earlier");

    fireEvent.click($("#btn-thread-more")!);
    expect($$(".bubble-row")).toHaveLength(12);
    expect($("#btn-thread-more")).toBeNull();
  });

  it("sends from the bottom, clears the draft, and ticks as acks arrive", async () => {
    openWith([inbound(BOB, "hi alice", 1)]);
    render(<App />);
    const input = $<HTMLTextAreaElement>("#thread-input")!;
    fireEvent.change(input, { target: { value: "reply from the thread" } });
    fireEvent.click($("#btn-thread-send")!);

    await waitFor(() => expect($(".bubble-row.mine")?.textContent).toContain("reply from the thread"));
    expect(input.value).toBe("");
    expect($(".bubble-row.mine .bubble-tick")!.getAttribute("aria-label")).toBe("Sent");

    act(() => useData.setState({ acks: [{ id: "k1", message_id: "sent-1", state: "read", timestamp: "2026-09-15T10:00:05Z" }] }));
    expect($(".bubble-row.mine .bubble-tick")!.getAttribute("aria-label")).toBe("Read");
  });

  it("a message arriving while open appears without losing the draft", async () => {
    openWith([inbound(BOB, "hi alice", 1)]);
    render(<App />);
    const input = $<HTMLTextAreaElement>("#thread-input")!;
    input.focus();
    fireEvent.change(input, { target: { value: "half-typed" } });

    act(() => useData.setState((state) => ({ messages: [...state.messages, inbound(BOB, "arrived while open", 2)] })));
    expect($$(".bubble-row.theirs")).toHaveLength(2);
    expect($<HTMLTextAreaElement>("#thread-input")).toBe(input);
    expect(input.value).toBe("half-typed");
    expect(document.activeElement).toBe(input);
  });

  it("an attachment bubble offers to open it", async () => {
    openWith([inbound(BOB, "report", 1, { type: "chat.attachment", metadata: { attachment_name: "evidence.txt" } })]);
    render(<App />);
    expect($(".bubble-text")!.textContent).toBe("evidence.txt");
    expect($(".bubble-text svg")).toBeTruthy();
    fireEvent.click($(".bubble-attachment")!);
    await waitFor(() => expect(holder.client.downloadAttachment).toHaveBeenCalledWith({ attachment_name: "evidence.txt" }));
  });

  it("back leaves the conversation", () => {
    openWith([]);
    render(<App />);
    expect($(".empty-state-title")!.textContent).toBe("No messages yet");
    fireEvent.click($("#btn-back")!);
    expect(useRoute.getState().sub).toBeNull();
    expect(useData.getState().thread).toBeNull();
  });
});
