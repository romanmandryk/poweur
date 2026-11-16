/**
 * Contacts and inbox policy from this device's saved copy when the relay
 * can't be read (EPIC-029 E29-T4).
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";

const holder = vi.hoisted(() => ({ client: null as any, snapshots: new Map<string, unknown>() }));
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
// The real snapshot store is encrypted IndexedDB; here it is a map.
vi.mock("../../src/lib/snapshot", () => ({
  saveSnapshot: vi.fn(async (identity: string, name: string, value: unknown) => {
    holder.snapshots.set(`${identity}:${name}`, JSON.parse(JSON.stringify(value)));
  }),
  loadSnapshot: vi.fn(async (identity: string, name: string) => holder.snapshots.get(`${identity}:${name}`) ?? null),
  clearSnapshots: vi.fn(async () => {}),
}));

import { loadPolicy } from "../../src/actions/account";
import { loadContacts, resetContactsForTests } from "../../src/actions/contacts";
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
const relayDown = async () => {
  throw new TypeError("Failed to fetch");
};

beforeEach(() => {
  resetStores();
  resetMessagingForTests();
  resetContactsForTests();
  holder.snapshots.clear();
  holder.client = fakeClient();
  useSession.setState({ identity: ME, unlocked: true });
  useRoute.setState({ page: "contacts" });
});

describe("saved contacts (E29-T4)", () => {
  it("saves the list whenever it is read from the relay", async () => {
    holder.client.contactsApi.load.mockResolvedValue({ contacts: [{ identity: BOB, state: "accepted", petname: "Bobby" }] });
    await loadContacts();
    const saved = holder.snapshots.get(`${ME}:contacts`) as any;
    expect(saved.list).toEqual([{ identity: BOB, petname: "Bobby", state: "accepted", pinnedKey: null }]);
    expect(saved.syncedAt).toBeGreaterThan(0);
    expect(useData.getState().contacts).toMatchObject({ loaded: true, saved: false });
  });

  it("shows the saved copy read-only, with when it was last synced, when the relay is down", async () => {
    holder.snapshots.set(`${ME}:contacts`, {
      list: [{ identity: BOB, petname: "Bobby", state: "accepted", pinnedKey: "ed25519:AAAA" }],
      syncedAt: Date.now() - 2 * 3_600_000,
    });
    holder.client.contactsApi.load.mockImplementation(relayDown);

    render(<App />);
    await waitFor(() => expect($$(".contact-row")).toHaveLength(1));
    await waitFor(() => expect(useData.getState().contacts.loading).toBe(false));

    expect($("#contacts-saved-note")!.textContent).toContain("last synced 2h ago");
    // The note explains it; no second, scarier warning.
    expect($(".val-warn")).toBeNull();
    expect($<HTMLButtonElement>("#btn-add-contact")!.disabled).toBe(true);

    fireEvent.click($(`[data-contact-menu="${BOB}"]`)!);
    await waitFor(() => expect($("#cp-read-only")).not.toBeNull());
    for (const id of ["cp-save-petname", "cp-block", "cp-remove"]) {
      expect($<HTMLButtonElement>(`#${id}`)!.disabled).toBe(true);
    }
    // Messaging a saved contact still works: it goes to the outbox.
    expect($<HTMLButtonElement>("#cp-message")!.disabled).toBe(false);
  });

  it("replaces the saved copy as soon as the relay answers", async () => {
    holder.snapshots.set(`${ME}:contacts`, { list: [{ identity: BOB, petname: null, state: "accepted", pinnedKey: null }], syncedAt: 1 });
    holder.client.contactsApi.load.mockImplementation(relayDown);
    await loadContacts();
    expect(useData.getState().contacts).toMatchObject({ saved: true, loaded: false });

    holder.client.contactsApi.load.mockResolvedValue({ contacts: [{ identity: BOB, state: "accepted" }, { identity: "carol.poweur.net", state: "requested" }] });
    await loadContacts({ force: true });
    const contacts = useData.getState().contacts;
    expect(contacts).toMatchObject({ saved: false, loaded: true });
    expect(contacts.list).toHaveLength(2);
  });

  it("says the read failed when there is no saved copy", async () => {
    holder.client.contactsApi.load.mockImplementation(relayDown);
    await loadContacts();
    expect(useData.getState().contacts).toMatchObject({ saved: false, list: [] });
    expect(useData.getState().contacts.error).toContain("Could not read contacts");
  });

  it("never shows another identity's saved contacts", async () => {
    holder.snapshots.set(`mallory.poweur.net:contacts`, { list: [{ identity: BOB, petname: null, state: "accepted", pinnedKey: null }], syncedAt: 1 });
    holder.client.contactsApi.load.mockImplementation(relayDown);
    await loadContacts();
    expect(useData.getState().contacts.list).toEqual([]);
  });
});

describe("saved inbox policy (E29-T4)", () => {
  const policy = { version: 1, mode: "contacts", anonymous: { allow: false }, read_receipts: "contacts" };

  it("saves the policy on read and falls back to it when the relay is down", async () => {
    holder.client.policy = vi.fn(async () => ({ policy, explicit: true }));
    await loadPolicy();
    expect(holder.snapshots.get(`${ME}:policy`)).toEqual({ policy, explicit: true });
    expect(useData.getState().policy).toMatchObject({ loaded: true, saved: false });

    // A fresh start with the relay unreachable.
    useData.setState((state) => ({ policy: { ...state.policy, doc: null, loaded: false } }));
    holder.client.policy = vi.fn(relayDown);
    await loadPolicy();
    const state = useData.getState().policy;
    expect(state.doc).toEqual(policy);
    // Not "loaded": the next visit asks the relay again.
    expect(state).toMatchObject({ saved: true, explicit: true, loaded: false, loading: false });
  });
});
