import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";

const holder = vi.hoisted(() => ({ client: null as any, enroll: null as any }));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => holder.client,
  identityApiFor: () => ({
    root: () => Promise.resolve({ version: "0.1.7", buildTime: "2026-09-15 10:00", versionHash: "abcdef1234" }),
    health: () => Promise.resolve({ status: "ok" }),
  }),
  lookup: vi.fn(() =>
    Promise.resolve({
      source: "well-known",
      document: { identity: "bob.poweur.net", public_key: "ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo", relay: "https://poweur.net", capabilities: ["messaging"] },
    }),
  ),
}));
vi.mock("../../src/lib/profiles.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  resolveProfile: vi.fn((identity: string) => Promise.resolve({ identity, links: [], capabilities: { features: { messaging: "1", files: "1" } } })),
  primeProfile: vi.fn(),
}));
vi.mock("../../src/lib/keystore.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  listEnrollments: vi.fn(() =>
    Promise.resolve([
      { enrollment_id: "e1", kind: "native", wrap: "native", current: true, label: "This phone" },
      { enrollment_id: "e2", kind: "passkey", wrap: "prf", current: false, label: "Laptop" },
    ]),
  ),
  removeEnrollment: vi.fn(() => Promise.resolve()),
}));
vi.mock("../../src/actions/identity", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  enrollApiForJoin: vi.fn(() => Promise.resolve({ enroll: holder.enroll, relayUrl: "http://127.0.0.1:8080" })),
}));
vi.mock("@poweur/client", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  streamForever: vi.fn(() => new Promise(() => {})),
}));

import { resetContactsForTests } from "../../src/actions/contacts";
import { resetMessagingForTests } from "../../src/actions/messages";
import { removeEnrollment } from "../../src/lib/keystore.js";
import { getActiveIdentity, loadIdentityRecord, saveIdentityRecord, setUnlockedKeys } from "../../src/lib/storage.js";
import { toBase64url } from "../../src/lib/vault.js";
import { App } from "../../src/shell/App";
import { useRoute } from "../../src/state/route";
import { useSession } from "../../src/state/session";
import { fakeClient } from "../helpers/fake-client";
import { resetStores } from "../helpers/stores";

const ME = "alice.poweur.net";
const SEED = toBase64url(new Uint8Array(32).fill(5));
const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);

beforeEach(() => {
  resetStores();
  resetMessagingForTests();
  resetContactsForTests();
  let policy: any = { version: 1, mode: "open" };
  let explicit = false;
  let granted = false;
  holder.client = fakeClient({
    policy: vi.fn(async () => ({ policy, explicit })),
    setPolicy: vi.fn(async (mode: string, anonymous: any, read_receipts: any) => {
      policy = { version: 1, mode, ...(anonymous ? { anonymous } : {}), read_receipts };
      explicit = true;
    }),
    profile: vi.fn(async () => ({ profile: null, explicit: false })),
    setProfile: vi.fn(async (doc: any) => doc),
    analyticsPreference: vi.fn(async () => ({ granted })),
    setAnalyticsConsent: vi.fn(async (value: boolean) => {
      granted = value;
    }),
  });
  holder.client.dav = vi.fn(async () => ({ devices: async () => ({ devices: [{ id: "d1", name: "CLI on server", kind: "agent" }] }), write: vi.fn() }));
  holder.enroll = {
    pending: vi.fn(async () => ({ sas: "482913", label: "New phone" })),
    approve: vi.fn(async () => {}),
  };
  saveIdentityRecord(ME, {
    identity: ME,
    publicKey: "ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    encPublicKey: "x25519:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
    relay: "http://127.0.0.1:8080",
    createdAt: "2026-09-15T10:00:00Z",
    encryptedKeys: { kdf: "native" },
    seedDerived: true,
    hosted: true,
  });
  // storage.js types `seed = null`; it takes a base64url seed.
  (setUnlockedKeys as unknown as (identity: string, s: unknown, e: unknown, seed: string) => void)(ME, { kty: "OKP" }, { kty: "OKP" }, SEED);
  // Active in storage too: saving config re-reads the session from storage.
  localStorage.setItem("poweur:active", ME);
  useSession.setState({ identity: ME, unlocked: true });
  useRoute.setState({ page: "settings" });
});

describe("Settings destination (E21-T11)", () => {
  it("shows the identity, its custody, and every group", async () => {
    render(<App />);
    expect($(".settings-id-name")!.textContent).toBe("alice");
    expect($(".settings-id-card .chip")!.textContent).toBe("🛡️ Device keystore");
    for (const id of ["row-switch-id", "row-profile", "row-identity-keys", "row-keys-devices", "row-connected-apps", "row-auth-request", "row-recovery-kit", "row-analytics", "row-policy", "row-policy-anon", "row-relay", "row-lookup", "row-session", "row-rotate-enc", "row-remove-id"]) {
      expect($(`#${id}`), id).toBeTruthy();
    }
    // A hosted identity has no DNS credentials to configure (E15-T10).
    expect($("#row-dns")).toBeNull();
    expect($("#row-session .settings-row-value")!.textContent).toBe("None");
    expect($("#row-recovery-kit .settings-row-value")!.textContent).toBe("Available");
    await waitFor(() => expect($("#row-profile .settings-row-value")!.textContent).toBe("Not set"));
    await waitFor(() => expect($("#about-relay-version")!.textContent).toBe("0.1.7"));
    expect($("#about-app-version")!.textContent).toMatch(/^\d+\.\d+\.\d+$/);
  });

  it("saves an inbox policy and the rows report what was saved", async () => {
    render(<App />);
    await waitFor(() => expect($("#row-policy .settings-row-value")!.textContent).toBe("Anyone"));
    fireEvent.click($("#row-policy")!);
    await waitFor(() => expect($("#policy-save")).toBeTruthy());

    fireEvent.click($('[data-mode="contacts_and_requests"]')!);
    fireEvent.click($("#policy-anon-allow")!);
    fireEvent.click($('[data-challenge="pow"]')!);
    fireEvent.change($("#policy-pow-bits")!, { target: { value: "8" } });
    fireEvent.click($("#policy-save")!);

    await waitFor(() => expect($("#panel-root")).toBeNull());
    expect(holder.client.setPolicy).toHaveBeenCalledWith(
      "contacts_and_requests",
      { allow: true, challenge: "pow", pow_bits: 8, max_bytes: 4096, max_per_day: 20 },
      { enabled: true, disabled_for: [] },
    );
    await waitFor(() => expect($("#row-policy .settings-row-value")!.textContent).toBe("Contacts, and requests from others"));
    expect($("#row-policy-anon .settings-row-value")!.textContent).toBe("On · 8 bits");
  });

  it("edits the profile, and shows what the identity speaks", async () => {
    render(<App />);
    fireEvent.click($("#row-profile")!);
    await waitFor(() => expect($("#pe-name")).toBeTruthy());
    await waitFor(() => expect($(".profile-caps")?.textContent).toContain("files"));
    $<HTMLInputElement>("#pe-name")!.value = "Alice Example";
    fireEvent.click($("#pe-save")!);
    await waitFor(() => expect($("#row-profile .settings-row-value")!.textContent).toBe("Alice Example"));
    expect(holder.client.setProfile).toHaveBeenCalledWith(expect.objectContaining({ display_name: "Alice Example" }));
  });

  it("analytics consent defaults off and survives reopening", async () => {
    render(<App />);
    fireEvent.click($("#row-analytics")!);
    await waitFor(() => expect($<HTMLInputElement>("#analytics-consent")).toBeTruthy());
    expect($<HTMLInputElement>("#analytics-consent")!.checked).toBe(false);
    fireEvent.click($("#analytics-consent")!);
    fireEvent.click($("#analytics-save")!);
    await waitFor(() => expect($("#panel-root")).toBeNull());

    fireEvent.click($("#row-analytics")!);
    await waitFor(() => expect($<HTMLInputElement>("#analytics-consent")?.checked).toBe(true));
  });

  it("keys & devices talks about this device and the OS keystore, and removes another one", async () => {
    render(<App />);
    fireEvent.click($("#row-keys-devices")!);
    await waitFor(() => expect(document.querySelectorAll("#panel-root .enrollment-row")).toHaveLength(3));
    const panel = $("#panel-root")!;
    expect(panel.textContent).toContain("this device");
    expect(panel.textContent).toContain("the OS keystore");
    expect(panel.textContent).toContain("CLI on server");
    // This device is enrolled, so no backup nag; and its own row cannot be removed.
    expect($("#btn-enroll-this")).toBeNull();
    expect($<HTMLButtonElement>('[data-remove-enrollment="e1"]')!.disabled).toBe(true);

    fireEvent.click($('[data-remove-enrollment="e2"]')!);
    await waitFor(() => expect($("#dialog-confirm")).toBeTruthy());
    fireEvent.click($("#dialog-confirm")!);
    await waitFor(() => expect(removeEnrollment).toHaveBeenCalledWith(holder.client, ME, "e2"));
  });

  it("approves a new device only after showing the six digits", async () => {
    render(<App />);
    fireEvent.click($("#row-keys-devices")!);
    await waitFor(() => expect($("#btn-enroll-device")).toBeTruthy());
    fireEvent.click($("#btn-enroll-device")!);
    await waitFor(() => expect($("#enroll-rendezvous")).toBeTruthy());

    $<HTMLInputElement>("#enroll-rendezvous")!.value = "  rv-123-abc \n";
    fireEvent.click($("#btn-enroll-lookup")!);
    await waitFor(() => expect($(".sas-code")?.textContent).toBe("482913"));
    expect(holder.enroll.pending).toHaveBeenCalledWith(holder.client.signer, ME, "rv-123-abc");
    expect(holder.enroll.approve).not.toHaveBeenCalled();

    fireEvent.click($("#btn-enroll-approve")!);
    await waitFor(() => expect(holder.enroll.approve).toHaveBeenCalled());
    await waitFor(() => expect($("#panel-root")).toBeNull());
  });

  it("a wrong request code is refused rather than half-approved", async () => {
    holder.enroll.pending.mockRejectedValueOnce(new Error("not found"));
    render(<App />);
    fireEvent.click($("#row-keys-devices")!);
    await waitFor(() => expect($("#btn-enroll-device")).toBeTruthy());
    fireEvent.click($("#btn-enroll-device")!);
    await waitFor(() => expect($("#enroll-rendezvous")).toBeTruthy());
    $<HTMLInputElement>("#enroll-rendezvous")!.value = "not-a-real-rendezvous-id";
    fireEvent.click($("#btn-enroll-lookup")!);
    await waitFor(() => expect($("#toast-root")!.textContent).toContain("No pending device"));
    expect($("#btn-enroll-approve")).toBeNull();
  });

  it("shows the recovery kit and checks it typed back", async () => {
    render(<App />);
    fireEvent.click($("#row-recovery-kit")!);
    await waitFor(() => expect(document.querySelectorAll(".mnemonic-word")).toHaveLength(24));
    const words = [...document.querySelectorAll(".mnemonic-word")].map((node) => node.textContent).join(" ");

    fireEvent.click($("#btn-verify-kit")!);
    $<HTMLTextAreaElement>("#kit-input")!.value = "wrong words";
    fireEvent.click($("#btn-check-kit")!);
    expect($("#kit-result")!.textContent).toContain("doesn't match");

    $<HTMLTextAreaElement>("#kit-input")!.value = words;
    fireEvent.click($("#btn-check-kit")!);
    expect($("#kit-result")!.textContent).toContain("That's your kit");
  });

  it("looks up an identity and shows its safety number", async () => {
    render(<App />);
    fireEvent.click($("#row-lookup")!);
    await waitFor(() => expect($("#lookup-id")).toBeTruthy());
    $<HTMLInputElement>("#lookup-id")!.value = "Bob.poweur.net";
    fireEvent.click($("#btn-lookup-run")!);
    await waitFor(() => expect($("#lookup-result")!.textContent).toContain("source: well-known"));
    expect($("#lookup-result")!.textContent).toMatch(/safety number: \d{5} \d{5} \d{5} \d{5}/);
  });

  it("saves a relay URL and the row shows it", async () => {
    render(<App />);
    fireEvent.click($("#row-relay")!);
    await waitFor(() => expect($("#panel-relay-url")).toBeTruthy());
    fireEvent.click($("#panel-test-relay")!);
    await waitFor(() => expect($("#panel-relay-status")!.textContent).toBe("✓ Connected"));
    $<HTMLInputElement>("#panel-relay-url")!.value = "https://relay.example.org";
    fireEvent.click($("#panel-save-relay")!);
    await waitFor(() => expect($("#row-relay .settings-row-value")!.textContent).toBe("https://relay.example.org"));
  });

  it("removes this identity from the device, and only on confirm", async () => {
    render(<App />);
    fireEvent.click($("#row-remove-id")!);
    await waitFor(() => expect($("#panel-confirm-remove")).toBeTruthy());
    expect($("#panel-root")!.textContent).toContain("only removes the local record");

    await act(async () => {
      fireEvent.click($("#panel-confirm-remove")!);
    });
    expect(loadIdentityRecord(ME)).toBeNull();
    expect(getActiveIdentity()).toBeNull();
    expect(useSession.getState().identity).toBeNull();
    expect(useRoute.getState().page).toBe("messages");
    await waitFor(() => expect($("#btn-welcome-start")).toBeTruthy());
  });

  it("Approve sign-in request starts from an empty request", async () => {
    render(<App />);
    fireEvent.click($("#row-auth-request")!);
    expect(useRoute.getState().sub).toBe("auth");
    await waitFor(() => expect($("#auth-request-input")).toBeTruthy());
  });
});
