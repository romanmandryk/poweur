/**
 * The Email tray and the subject views in the shell (EPIC-023 E23-T6,
 * EPIC-009 E09-T13).
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";

const holder = vi.hoisted(() => ({ client: null as any }));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => holder.client,
}));
vi.mock("@poweur/client", async (importOriginal) => ({
  ...(await importOriginal<object>()),
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
const BRIDGE = "email.poweur.net";
const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);
/** Wait until a selector matches, and return the element. */
const found = <T extends Element = HTMLElement>(selector: string) =>
  waitFor(() => {
    const el = document.querySelector<T>(selector);
    expect(el, selector).toBeTruthy();
    return el!;
  });

const mail = (id: string, at: number, extra: Record<string, unknown> = {}) =>
  inbound(BRIDGE, JSON.stringify({
    version: 1, from: { name: "Ann", address: "ann@example.com" }, to: [{ address: "alice@poweur.net" }], subject: "Lunch",
    message_id: `${id}@example.com`, auth: { verdict: "verified" }, text: "Friday?",
    html: `<p onclick="x()">Friday?</p><script>steal()</script><img src="https://t.example/p.gif">`, ...extra,
  }), at, { id, type: "email.message" });

beforeEach(() => {
  resetStores();
  resetMessagingForTests();
  resetContactsForTests();
  holder.client = fakeClient();
  useSession.setState({ identity: ME, unlocked: true });
});

function optedIn() {
  holder.client.policy.mockResolvedValue({ policy: { version: 1, mode: "open", email: { allow: true, bridge: BRIDGE } }, explicit: true });
  holder.client.emailApi.state.mockResolvedValue({ enabled: true, address: "alice@poweur.net", bridge: BRIDGE, optIn: {} });
  holder.client.emailApi.offeredBridge.mockResolvedValue(BRIDGE);
}

describe("Email tray", () => {
  it("is hidden when the relay offers no bridge", async () => {
    render(<App />);
    await waitFor(() => expect(holder.client.policy).toHaveBeenCalled());
    expect($('.tray-tab[data-tray="email"]')).toBeNull();
  });

  it("after opting out, offers to turn email back on", async () => {
    holder.client.emailApi.offeredBridge.mockResolvedValue(BRIDGE);
    holder.client.policy.mockResolvedValue({ policy: { version: 1, mode: "open", email: { allow: false } }, explicit: true });
    render(<App />);
    const tab = await found('.tray-tab[data-tray="email"]');
    fireEvent.click(tab);
    const button = await found("#btn-email-enable");
    expect($("#email-optin")!.textContent).toContain("alice@poweur.net");
    fireEvent.click(button);
    await waitFor(() => expect(holder.client.emailApi.enable).toHaveBeenCalled());
  });

  it("is on by default: the relay's bridge delivers mail with no email block in the policy", async () => {
    holder.client.emailApi.offeredBridge.mockResolvedValue(BRIDGE);
    holder.client.emailApi.state.mockResolvedValue({ enabled: true, address: "alice@poweur.net", bridge: BRIDGE, optIn: null, byDefault: true } as any);
    holder.client.inboxAndArchive.mockResolvedValue({ messages: [mail("e1", 1)], acks: [], lost: 0 });
    render(<App />);
    fireEvent.click(await found('.tray-tab[data-tray="email"]'));
    expect((await found(".mail-row")).querySelector(".mail-subject")!.textContent).toBe("Lunch");
    expect($(`.conv-row[data-compose-to="${BRIDGE}"]`)).toBeNull();
  });

  it("blocks a sender from an open email", async () => {
    optedIn();
    holder.client.inboxAndArchive.mockResolvedValue({ messages: [mail("e1", 1)], acks: [], lost: 0 });
    render(<App />);
    fireEvent.click(await found('.tray-tab[data-tray="email"]'));
    fireEvent.click(await found(".mail-row"));
    fireEvent.click(await found(".mail-block"));
    fireEvent.click(await found("#confirm-email-block"));
    await waitFor(() => expect(holder.client.emailApi.saveSettings).toHaveBeenCalledWith({ receive: "everyone", block: ["ann@example.com"] }));
  });

  it("keeps mail out of chat, lists it, and renders it sandboxed", async () => {
    optedIn();
    holder.client.inboxAndArchive.mockResolvedValue({
      messages: [mail("e1", 1), inbound("bob.poweur.net", "chat", 2), mail("fake", 3, {}), { ...mail("forged", 4), sender: "mallory.poweur.net" }],
      acks: [], lost: 0,
    });
    render(<App />);
    await waitFor(() => expect($('.conv-row[data-compose-to="bob.poweur.net"]')).toBeTruthy());
    // The bridge's mail is not a chat with the bridge; a forgery stays in chat, untrusted.
    expect($(`.conv-row[data-compose-to="${BRIDGE}"]`)).toBeNull();
    expect($('.conv-row[data-compose-to="mallory.poweur.net"]')!.textContent).toContain("app message");

    fireEvent.click($('.tray-tab[data-tray="email"]')!);
    const row = await found(".mail-row");
    expect(row.querySelector(".mail-subject")!.textContent).toBe("Lunch");
    fireEvent.click(row);
    expect(useRoute.getState().sub).toBe("email-thread");
    const frame = await found<HTMLIFrameElement>("iframe.email-frame");
    expect(frame.getAttribute("sandbox")).not.toContain("allow-scripts");
    const doc = frame.getAttribute("srcdoc")!;
    expect(doc).not.toContain("<script");
    expect(doc).not.toContain("onclick");
    expect(doc).not.toContain("t.example");
    expect($("#btn-email-load-images")).toBeTruthy();
  });

  it("says old email is cleaned up automatically", async () => {
    optedIn();
    render(<App />);
    fireEvent.click(await found('.tray-tab[data-tray="email"]'));
    expect((await found("#email-cleanup-note")).textContent).toContain("last 7 days");
  });

  it("composes and sends through the bridge", async () => {
    optedIn();
    render(<App />);
    await waitFor(() => expect($('.tray-tab[data-tray="email"]')).toBeTruthy());
    fireEvent.click($('.tray-tab[data-tray="email"]')!);
    fireEvent.click(await found("#btn-email-compose"));
    fireEvent.change($("#email-to")!, { target: { value: "Ann <ann@example.com>" } });
    fireEvent.change($("#email-subject")!, { target: { value: "Hi" } });
    fireEvent.change($("#email-text")!, { target: { value: "Hello Ann" } });
    fireEvent.click($("#btn-email-send")!);
    await waitFor(() => expect(holder.client.emailApi.send).toHaveBeenCalledWith(expect.objectContaining({
      to: [{ name: "Ann", address: "ann@example.com" }], subject: "Hi", text: "Hello Ann",
    })));
    await waitFor(() => expect(useRoute.getState().sub).toBe(null));
    expect(useData.getState().messages.some((m: any) => m.type === "email.send")).toBe(true);
  });
});

describe("a send the bridge never answers", () => {
  const sent = (id: string, at: number, sendId: string, sender = ME) =>
    inbound(sender, JSON.stringify({ version: 1, send_id: sendId, to: [{ address: "ann@example.com" }], subject: "Hi", text: "Hello" }), at,
      { id, type: "email.send", recipient: BRIDGE });

  it("stops saying Sending… and offers to send it again under the same send id", async () => {
    optedIn();
    holder.client.inboxAndArchive.mockResolvedValue({ messages: [sent("s1", 1, "01M52V6GAPWEZ7M1A5CF098QJ8")], acks: [], lost: 0 });
    render(<App />);
    await waitFor(() => expect($('.tray-tab[data-tray="email"]')).toBeTruthy());
    fireEvent.click($('.tray-tab[data-tray="email"]')!);
    const row = await found(".mail-row");
    expect(row.textContent).toContain("No reply from the email service");
    fireEvent.click(row);
    const card = await found(".mail-no-reply");
    fireEvent.click(card.querySelector("button")!);
    await waitFor(() => expect(holder.client.emailApi.send).toHaveBeenCalledWith(expect.objectContaining({
      send_id: "01M52V6GAPWEZ7M1A5CF098QJ8", subject: "Hi", text: "Hello",
    })));
    expect(holder.client.emailApi.send.mock.calls[0][0]).not.toHaveProperty("version");
    // The fresh copy stands for the send: one card, waiting again.
    await waitFor(() => expect($(".mail-no-reply")).toBeNull());
    expect(document.querySelectorAll(".mail-card")).toHaveLength(1);
    expect($(".mail-send-state")!.textContent).toBe("Sending…");
  });
});

describe("Inbox by subject (E09-T13)", () => {
  it("toggles the view and opens a thread with its subject prefilled", async () => {
    holder.client.inboxAndArchive.mockResolvedValue({
      messages: [
        inbound("bob.poweur.net", "numbers", 1, { thread_id: "01JA", subject: "Q3 budget" }),
        inbound("bob.poweur.net", "hello", 2),
      ],
      acks: [], lost: 0,
    });
    render(<App />);
    await waitFor(() => expect(document.querySelectorAll(".conv-row")).toHaveLength(1));
    fireEvent.click($('[data-view="subject"]')!);
    await waitFor(() => expect(document.querySelectorAll(".conv-row")).toHaveLength(2));
    const q3 = $('.conv-row[data-thread="01JA"]')!;
    expect(q3.querySelector(".conv-subject")!.textContent).toBe("Q3 budget");
    fireEvent.click(q3);
    const field = await found<HTMLInputElement>("#thread-subject");
    expect(field.value).toBe("Q3 budget");
    fireEvent.change(field, { target: { value: "Q4 budget" } });
    expect($("#subject-starts-new")).toBeTruthy();
    expect(localStorage.getItem("poweur:tray-view:alice.poweur.net")).toBe("subject");
  });
});

describe("live mail", () => {
  it("shows mail that arrives while the tray is open", async () => {
    optedIn();
    render(<App />);
    fireEvent.click(await found('.tray-tab[data-tray="email"]'));
    await found("#btn-email-compose");
    const { mergeMessages } = await import("../../src/actions/messages");
    mergeMessages([mail("late", 9)]);
    await found(".mail-row");
  });
});

describe("onboarding", () => {
  it("names the address on the last step — email is on by default", async () => {
    holder.client.emailApi.offeredBridge.mockResolvedValue(BRIDGE);
    holder.client.emailApi.state.mockResolvedValue({ enabled: true, address: "alice@poweur.net", bridge: BRIDGE, optIn: null, byDefault: true } as any);
    useData.setState({ onboard: { step: 3 } });
    useRoute.setState({ sub: "onboarding" });
    render(<App />);
    expect((await found("#onboard-email")).textContent).toContain("alice@poweur.net");
    expect($("#email-optin")).toBeNull();
  });
});

describe("receive-only bridge", () => {
  it("hides writing and replying", async () => {
    optedIn();
    holder.client.emailApi.capabilities.mockResolvedValue({ send: false });
    holder.client.inboxAndArchive.mockResolvedValue({ messages: [mail("e1", 1)], acks: [], lost: 0 });
    render(<App />);
    fireEvent.click(await found('.tray-tab[data-tray="email"]'));
    fireEvent.click(await found(".mail-row"));
    await found("iframe.email-frame");
    expect($("#btn-email-compose")).toBeNull();
    expect($("#btn-email-reply")).toBeNull();
  });
});

describe("sending paused", () => {
  it("warns in compose but still sends (the bridge decides)", async () => {
    optedIn();
    holder.client.emailApi.capabilities.mockResolvedValue({ send: true, sendPaused: true });
    render(<App />);
    fireEvent.click(await found('.tray-tab[data-tray="email"]'));
    expect((await found(".empty-state")).textContent).toContain("sign up to apps");
    fireEvent.click(await found("#btn-email-compose"));
    expect((await found("#email-send-paused")).textContent).toContain("paused");
    fireEvent.change($("#email-to")!, { target: { value: "ann@example.com" } });
    fireEvent.click($("#btn-email-send")!);
    await waitFor(() => expect(holder.client.emailApi.send).toHaveBeenCalled());
  });

  it("shows no warning when sending is on", async () => {
    optedIn();
    render(<App />);
    fireEvent.click(await found('.tray-tab[data-tray="email"]'));
    fireEvent.click(await found("#btn-email-compose"));
    await found("#email-to");
    expect($("#email-send-paused")).toBeNull();
  });
});

describe("receive filters", () => {
  it("shows the newest saved settings and changes who may email", async () => {
    optedIn();
    const { openEmailPanel } = await import("../../src/screens/settings/panels");
    const { mergeMessages } = await import("../../src/actions/messages");
    render(<App />);
    await waitFor(() => expect(useData.getState().email.loaded).toBe(true));
    mergeMessages([
      { id: "s1", sender: ME, recipient: BRIDGE, timestamp: "2026-10-14T09:00:00Z", type: "email.settings", queue: "sent",
        plaintext: JSON.stringify({ version: 1, block: ["old@example.com"], updated_at: "2026-10-14T09:00:00Z" }) },
      { id: "s2", sender: ME, recipient: BRIDGE, timestamp: "2026-10-14T10:00:00Z", type: "email.settings", queue: "sent",
        plaintext: JSON.stringify({ version: 1, block: ["pest@example.com"], updated_at: "2026-10-14T10:00:00Z" }) },
    ]);
    act(() => openEmailPanel());
    const blocked = await found("#email-blocked");
    expect(blocked.textContent).toContain("pest@example.com");
    expect(blocked.textContent).not.toContain("old@example.com");
    // Settings messages are not a chat with the bridge.
    expect($(`.conv-row[data-compose-to="${BRIDGE}"]`)).toBeNull();

    fireEvent.click($("#email-contacts-only")!);
    await waitFor(() => expect(holder.client.emailApi.saveSettings).toHaveBeenCalledWith({ receive: "contacts", block: ["pest@example.com"] }));
    fireEvent.change($("#email-filter-entry")!, { target: { value: "Spam.Example" } });
    fireEvent.click($("#btn-email-block")!);
    await waitFor(() => expect(holder.client.emailApi.saveSettings).toHaveBeenLastCalledWith(expect.objectContaining({ block: ["pest@example.com", "@spam.example"] })));
  });
});
