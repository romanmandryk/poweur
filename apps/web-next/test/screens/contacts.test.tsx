import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";

const holder = vi.hoisted(() => ({ client: null as any }));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => holder.client,
}));
vi.mock("../../src/lib/profiles.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  resolveProfile: vi.fn((identity: string) => Promise.resolve({ identity, displayName: null, links: [], capabilities: { features: {} } })),
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
import { fakeClient } from "../helpers/fake-client";
import { resetStores } from "../helpers/stores";

const ME = "alice.poweur.net";
const BOB = "bob.poweur.net";
const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);
const $$ = (selector: string) => document.querySelectorAll(selector);

/** A contacts document the fake relay serves, and mutates as the app writes it. */
function relayContacts(initial: any[]) {
  let list = [...initial];
  holder.client.contactsApi.load.mockImplementation(async () => ({ contacts: list }));
  holder.client.contactsApi.set.mockImplementation(async (identity: string, state: string, extra: any = {}) => {
    list = [...list.filter((c) => c.identity !== identity), { identity, state, ...(extra.petname ? { petname: extra.petname } : {}) }];
  });
  holder.client.contactsApi.remove.mockImplementation(async (identity: string) => {
    list = list.filter((c) => c.identity !== identity);
  });
  holder.client.requestContact.mockImplementation(async (identity: string, options: any = {}) => {
    list = [...list, { identity, state: "requested", ...(options.petname ? { petname: options.petname } : {}) }];
  });
  holder.client.blockContact.mockImplementation(async (identity: string) => {
    list = [...list.filter((c) => c.identity !== identity), { identity, state: "blocked" }];
  });
}

beforeEach(() => {
  resetStores();
  resetMessagingForTests();
  resetContactsForTests();
  holder.client = fakeClient();
  useSession.setState({ identity: ME, unlocked: true });
  useRoute.setState({ page: "contacts" });
});

describe("Contacts destination (E21-T8)", () => {
  it("lists contacts with their state and petname, and filters them", async () => {
    relayContacts([
      { identity: BOB, state: "accepted", petname: "Bobby", pinned_key: "ed25519:AAAA" },
      { identity: "carol.poweur.net", state: "requested" },
      { identity: "mallory.poweur.net", state: "blocked" },
    ]);
    render(<App />);
    await waitFor(() => expect($$(".contact-row")).toHaveLength(3));
    expect([...$$(".contact-row .chip")].map((chip) => chip.textContent)).toEqual(["Contact", "Requested", "Blocked"]);
    expect($(`[data-contact-open="${BOB}"] .profile-card-name`)!.textContent).toBe("Bobby");

    const filter = $<HTMLInputElement>("#contacts-filter")!;
    filter.focus();
    fireEvent.change(filter, { target: { value: "bob" } });
    expect($$(".contact-row")).toHaveLength(1);
    // The filter is the same node with focus kept — the legacy app rebuilt it per keystroke.
    expect(document.activeElement).toBe(filter);

    fireEvent.change(filter, { target: { value: "nobody" } });
    expect(document.body.textContent).toContain("No contact matches “nobody”.");
  });

  it("tapping a contact messages them; tapping a blocked one opens its panel", async () => {
    relayContacts([
      { identity: BOB, state: "accepted" },
      { identity: "mallory.poweur.net", state: "blocked" },
    ]);
    render(<App />);
    await waitFor(() => expect($$(".contact-row")).toHaveLength(2));

    fireEvent.click($('[data-contact-open="mallory.poweur.net"]')!);
    await waitFor(() => expect($("#cp-unblock")).toBeTruthy());
    expect(useRoute.getState().sub).toBeNull();
    fireEvent.click($("#panel-close-btn")!);
    await waitFor(() => expect($("#panel-root")).toBeNull());

    fireEvent.keyDown($(`[data-contact-open="${BOB}"]`)!, { key: "Enter" });
    expect(useRoute.getState().sub).toBe("thread");
    expect(useData.getState().thread).toEqual({ peer: BOB, threadId: "", group: false });
  });

  it("adds a contact by typed name, with an intro and a petname", async () => {
    relayContacts([]);
    render(<App />);
    await waitFor(() => expect($("#btn-add-contact-empty")).toBeTruthy());
    fireEvent.click($("#btn-add-contact-empty")!);
    await waitFor(() => expect($("#panel-root .idin input")).toBeTruthy());

    // A bare handle is completed with our own domain.
    $<HTMLInputElement>("#panel-root .idin input")!.value = "bob";
    $<HTMLInputElement>("#ac-intro")!.value = "hi, it's alice";
    $<HTMLInputElement>("#ac-petname")!.value = "Bobby";
    fireEvent.click($("#btn-add-contact-go")!);

    await waitFor(() => expect(holder.client.requestContact).toHaveBeenCalledWith(BOB, { intro: "hi, it's alice", petname: "Bobby" }));
    await waitFor(() => expect($(".contact-row .chip")?.textContent).toBe("Requested"));
    expect($(".contact-row .profile-card-name")!.textContent).toBe("Bobby");
    expect($("#panel-root")).toBeNull();
  });

  it("an ID that does not resolve sends nothing", async () => {
    relayContacts([]);
    const { resolveProfile } = await import("../../src/lib/profiles.js");
    vi.mocked(resolveProfile).mockRejectedValueOnce(new Error("identity not found"));
    render(<App />);
    fireEvent.click($("#btn-add-contact")!);
    await waitFor(() => expect($("#panel-root .idin input")).toBeTruthy());
    $<HTMLInputElement>("#panel-root .idin input")!.value = "ghost.poweur.net";
    fireEvent.click($("#btn-add-contact-go")!);
    await waitFor(() => expect($("#panel-root .idin-status")!.textContent).toContain("Not found"));
    expect(holder.client.requestContact).not.toHaveBeenCalled();
    expect($(".toast.warning")!.textContent).toContain("Enter a Poweur ID we can find");
  });

  it("Just message them opens the conversation without a request", async () => {
    relayContacts([]);
    render(<App />);
    fireEvent.click($("#btn-add-contact")!);
    await waitFor(() => expect($("#panel-root .idin input")).toBeTruthy());
    $<HTMLInputElement>("#panel-root .idin input")!.value = BOB;
    fireEvent.click($("#btn-add-contact-msg")!);
    await waitFor(() => expect(useRoute.getState().sub).toBe("thread"));
    expect(holder.client.requestContact).not.toHaveBeenCalled();
  });
});

describe("Contact panel", () => {
  const openPanelFor = async (identity: string) => {
    render(<App />);
    await waitFor(() => expect($(`[data-contact-menu="${identity}"]`)).toBeTruthy());
    fireEvent.click($(`[data-contact-menu="${identity}"]`)!);
    await waitFor(() => expect($("#panel-root")).toBeTruthy());
  };

  it("shows the safety number of the pinned key, and saves a petname", async () => {
    relayContacts([{ identity: BOB, state: "accepted", pinned_key: "ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo" }]);
    await openPanelFor(BOB);
    expect($("#panel-title")!.textContent).toBe("bob");
    expect($("#panel-root")!.textContent).toMatch(/\d{5} \d{5} \d{5} \d{5}/);
    // The row's own menu button is not a message tap.
    expect(useRoute.getState().sub).toBeNull();

    fireEvent.change($("#cp-petname")!, { target: { value: " Bobby " } });
    $<HTMLInputElement>("#cp-petname")!.value = " Bobby ";
    fireEvent.click($("#cp-save-petname")!);
    await waitFor(() => expect(holder.client.contactsApi.set).toHaveBeenCalledWith(BOB, "accepted", { petname: "Bobby" }));
    await waitFor(() => expect($(".contact-row .profile-card-name")?.textContent).toBe("Bobby"));
  });

  it("blocks, and unblocks without notifying anyone", async () => {
    relayContacts([{ identity: BOB, state: "accepted" }]);
    await openPanelFor(BOB);
    fireEvent.click($("#cp-block")!);
    await waitFor(() => expect($(".contact-row .chip")?.textContent).toBe("Blocked"));
    expect(holder.client.blockContact).toHaveBeenCalledWith(BOB);

    fireEvent.click($(`[data-contact-menu="${BOB}"]`)!);
    await waitFor(() => expect($("#cp-unblock")).toBeTruthy());
    fireEvent.click($("#cp-unblock")!);
    await waitFor(() => expect($(".contact-row .chip")?.textContent).toBe("Contact"));
    expect(holder.client.acceptContact).not.toHaveBeenCalled();
  });

  it("removes a contact", async () => {
    relayContacts([{ identity: BOB, state: "accepted" }]);
    await openPanelFor(BOB);
    await act(async () => {
      fireEvent.click($("#cp-remove")!);
    });
    await waitFor(() => expect($$(".contact-row")).toHaveLength(0));
    expect($(".empty-state-title")!.textContent).toBe("No contacts yet");
  });
});
