/**
 * The Email tray and the subject views in the shell (EPIC-023 E23-T6,
 * EPIC-009 E09-T13).
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";

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

  it("offers the address, and opting in turns it on", async () => {
    holder.client.emailApi.offeredBridge.mockResolvedValue(BRIDGE);
    render(<App />);
    const tab = await found('.tray-tab[data-tray="email"]');
    fireEvent.click(tab);
    const button = await found("#btn-email-enable");
    expect(button.textContent).toContain("alice@poweur.net");
    fireEvent.click(button);
    await waitFor(() => expect(holder.client.emailApi.enable).toHaveBeenCalled());
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
  it("offers the address on the last step when the relay has a bridge", async () => {
    holder.client.emailApi.offeredBridge.mockResolvedValue(BRIDGE);
    useData.setState({ onboard: { step: 3 } });
    useRoute.setState({ sub: "onboarding" });
    render(<App />);
    const offer = await found("#email-optin");
    expect(offer.textContent).toContain("alice@poweur.net");
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
