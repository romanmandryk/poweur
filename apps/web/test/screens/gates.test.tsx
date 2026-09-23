import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const fake = vi.hoisted(() => ({
  client: {
    setPolicy: vi.fn(() => Promise.resolve()),
    policy: vi.fn(() => Promise.resolve({ policy: { version: 1, mode: "contacts_and_requests" }, explicit: true })),
    setProfile: vi.fn(),
    sessions: { ensure: vi.fn(() => Promise.resolve({})) },
    dav: vi.fn(() => Promise.resolve({})),
    signer: {},
  },
  consent: vi.fn(),
  sign: vi.fn(() => Promise.resolve({ response: { request_id: "r" }, encoded: "ENCODED" })),
  deliver: vi.fn(),
}));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => fake.client,
}));
vi.mock("../../src/lib/passkey.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  checkPasskeySupport: vi.fn(() => Promise.resolve({ available: true, prf: true })),
  authenticatePasskey: vi.fn(() => Promise.resolve({ prfOutput: new Uint8Array(32) })),
  unwrapKeysWithPRF: vi.fn(() => Promise.resolve({ signingJWK: { kty: "OKP" }, encJWK: { kty: "OKP" }, seed: null })),
}));
vi.mock("../../src/lib/signin.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  loadSignInConsent: fake.consent,
  signBrowserApproval: fake.sign,
  appendBrowserConsent: vi.fn(() => Promise.resolve({})),
  deliverBrowserApproval: fake.deliver,
}));

import { authenticatePasskey } from "../../src/lib/passkey.js";
import { getActiveIdentity, saveIdentityRecord } from "../../src/lib/storage.js";
import { App } from "../../src/shell/App";
import { useData } from "../../src/state/data";
import { setContinueTo } from "../../src/actions/signin";
import { useRoute } from "../../src/state/route";
import { lockIdentity, useSession, type ModeInfo } from "../../src/state/session";
import { resetStores } from "../helpers/stores";

const IDENTITY = "alice.poweur.net";
const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);
const base: ModeInfo = { mode: "unknown", hostedDomains: ["poweur.net"], domain: "poweur.net", probed: true, reachable: true, resolved: true };
const at = (sub: Parameters<ReturnType<typeof useRoute.getState>["push"]>[0], params = {}) =>
  useRoute.setState({ page: "messages", sub, params });

beforeEach(() => {
  resetStores();
  vi.clearAllMocks();
  useSession.setState({ mode: base, config: { relayUrl: "http://127.0.0.1:8080" } });
  localStorage.setItem("poweur:config", JSON.stringify({ relayUrl: "http://127.0.0.1:8080", parentDomain: "poweur.net" }));
});
afterEach(() => {
  delete (globalThis as { Capacitor?: unknown }).Capacitor;
});

describe("Add identity, per door (E15-T9)", () => {
  it("an unknown host offers passkey sign-in, join and create", () => {
    at("add-id");
    render(<App />);
    expect($(".sub-title")!.textContent).toBe("Add identity");
    expect($("#signin-id-input")).toBeTruthy();
    expect($("#opt-join-device")).toBeTruthy();
    fireEvent.click($("#opt-create-new")!);
    expect(useRoute.getState().sub).toBe("claim");
    expect($("#claim-card")).toBeTruthy();
  });

  it("an identity host names its subject and takes no typed identity", () => {
    useSession.setState({ mode: { ...base, mode: "identity", subject: "bob.poweur.net", handle: "bob" } });
    at("add-id");
    render(<App />);
    expect($(".sub-title")!.textContent).toBe("Sign in");
    expect($("#btn-door-signin")).toBeTruthy();
    expect($("#opt-join-device")!.dataset.joinIdentity).toBe("bob.poweur.net");
    expect($("#signin-id-input")).toBeNull();
    expect($("#opt-create-new")).toBeNull();
  });

  it("the shell offers join and create, never a passkey, and asks which relay", () => {
    (globalThis as { Capacitor?: unknown }).Capacitor = { isNativePlatform: () => true };
    useSession.setState({ mode: { ...base, mode: "shell" } });
    at("add-id");
    render(<App />);
    expect($("#opt-join-device")).toBeTruthy();
    expect($("#opt-create-new")).toBeTruthy();
    expect($("#signin-id-input")).toBeNull();
    expect($("#btn-signin-passkey")).toBeNull();
    expect($("#btn-door-signin")).toBeNull();
    expect($<HTMLSelectElement>("#setup-relay-preset")).toBeTruthy();
    expect($("#opt-join-device")!.textContent).toContain("Add this device");
  });

  it("signing in to an identity this browser holds switches to it and asks to unlock", () => {
    saveIdentityRecord(IDENTITY, { identity: IDENTITY, encryptedKeys: { kdf: "prf" } });
    at("add-id");
    render(<App />);
    fireEvent.change($("#signin-id-input")!, { target: { value: " Alice.Poweur.NET " } });
    fireEvent.click($("#btn-signin-passkey")!);
    expect(getActiveIdentity()).toBe(IDENTITY);
    expect(useSession.getState().identity).toBe(IDENTITY);
    expect(useRoute.getState().sub).toBe("unlock");
  });
});

describe("Unlock", () => {
  beforeEach(() => {
    saveIdentityRecord(IDENTITY, { identity: IDENTITY, credentialId: "Y3JlZA", encryptedKeys: { kdf: "prf" } });
    useSession.setState({ identity: IDENTITY, unlocked: false });
  });

  it("opens the keys with the passkey, then returns to where it was asked from", async () => {
    at("unlock", { returnTo: "auth" });
    render(<App />);
    expect($("#btn-do-unlock")!.textContent).toContain("Unlock with passkey");
    expect($(".unlock-name")!.textContent).toBe("alice");

    fireEvent.click($("#btn-do-unlock")!);
    await waitFor(() => expect(useSession.getState().unlocked).toBe(true));
    expect(authenticatePasskey).toHaveBeenCalledWith("Y3JlZA", expect.objectContaining({ rpId: expect.any(String) }));
    expect(fake.client.sessions.ensure).toHaveBeenCalled();
    expect(useRoute.getState().sub).toBe("auth");
  });

  it("says what a device keystore will ask for", () => {
    saveIdentityRecord(IDENTITY, { identity: IDENTITY, encryptedKeys: { kdf: "native" } });
    at("unlock");
    render(<App />);
    expect($("#btn-do-unlock")!.textContent).toBe(" Unlock");
    expect($("#btn-do-unlock svg")).toBeTruthy();
    expect(screen.getByText("Your device will ask for Face ID, Touch ID or your passcode")).toBeTruthy();
  });

  it("with nothing to unlock on this device, offers to add an identity", () => {
    useSession.setState({ identity: "ghost.poweur.net" });
    at("unlock");
    render(<App />);
    expect(useRoute.getState().sub).toBe("add-id");
  });
});

describe("Onboarding (E15-T5)", () => {
  beforeEach(() => {
    useSession.setState({ identity: IDENTITY, unlocked: true });
    useData.setState({ onboard: { step: 1 } });
    at("onboarding");
  });

  it("walks policy → profile → done, saving only what was chosen", async () => {
    render(<App />);
    expect($(".onboard-title")!.textContent).toBe("Who can message you?");
    expect($(".policy-mode.selected")!.dataset.mode).toBe("contacts_and_requests");

    fireEvent.click($("#btn-onboard-next")!);
    await waitFor(() => expect($(".onboard-title")!.textContent).toBe("How should people see you?"));
    expect(fake.client.setPolicy).toHaveBeenCalledWith("contacts_and_requests", undefined, { enabled: true, disabled_for: [] });

    // Nothing typed: nothing written.
    fireEvent.click($("#btn-onboard-next")!);
    await waitFor(() => expect($(".onboard-title")!.textContent).toBe("You're set"));
    expect(fake.client.setProfile).not.toHaveBeenCalled();

    fireEvent.click($("#btn-onboard-back")!);
    expect($(".onboard-title")!.textContent).toBe("How should people see you?");
    fireEvent.click($("#btn-onboard-next")!);
    await waitFor(() => expect($("#btn-onboard-next")!.textContent).toBe("Start using Poweur"));
    fireEvent.click($("#btn-onboard-next")!);
    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });
    expect(useData.getState().onboard).toBeNull();
  });

  it("a failed save keeps the step, with the reason beside the field", async () => {
    fake.client.setPolicy.mockRejectedValueOnce(new Error("relay said no"));
    render(<App />);
    fireEvent.click($("#btn-onboard-next")!);
    await waitFor(() => expect(screen.getByText("relay said no")).toBeTruthy());
    expect($(".onboard-title")!.textContent).toBe("Who can message you?");
  });

  it("Skip finishes without writing anything", () => {
    render(<App />);
    fireEvent.click($("#btn-onboard-skip")!);
    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });
    expect(fake.client.setPolicy).not.toHaveBeenCalled();
  });

  it("does not inherit another identity's name, bio or inbox", () => {
    useData.setState({
      profile: { doc: { display_name: "John Example", bio: "john's bio" }, explicit: true, loaded: true, loading: false },
      messages: [{ id: "m1", plaintext: "john's secret" }],
      onboard: { step: 2 },
    });
    lockIdentity();
    useSession.setState({ identity: "dddddd.poweur.net", unlocked: true });
    useData.setState({ onboard: { step: 2 } });
    at("onboarding");
    render(<App />);
    expect($<HTMLInputElement>("#pe-name")!.value).toBe("");
    expect($<HTMLTextAreaElement>("#pe-bio")!.value).toBe("");
    expect($("#pe-name")!.getAttribute("placeholder")).toBe("dddddd");
    expect(useData.getState().messages).toEqual([]);
  });
});

describe("Sign-in approval (EPIC-008)", () => {
  it("checks a pasted request, then asks to unlock before approving", async () => {
    // Unlock redirects to add-id when this device holds no record for the identity.
    saveIdentityRecord(IDENTITY, { identity: IDENTITY, encryptedKeys: { kdf: "prf" } });
    useSession.setState({ identity: IDENTITY, unlocked: false });
    fake.consent.mockResolvedValueOnce({
      request: { audience: "app.example", statement: "Welcome back" },
      metadata: { name: "Example App" },
      headline: "Sign in to Example App",
      scopes: [],
    });
    at("auth");
    render(<App />);

    fireEvent.change($("#auth-request-input")!, { target: { value: "poweur://auth?request=abc123" } });
    fireEvent.click($("#btn-auth-load")!);
    await waitFor(() => expect(screen.getByText("Sign in to Example App")).toBeTruthy());
    expect(fake.consent).toHaveBeenCalledWith("abc123");
    expect(screen.getByText("Origin verified")).toBeTruthy();
    expect(screen.getByText("This app requests sign-in only, with no home access.")).toBeTruthy();

    fireEvent.click($("#btn-auth-unlock")!);
    expect(useRoute.getState()).toMatchObject({ sub: "unlock", params: { returnTo: "auth" } });
  });

  it("shows why a request was refused", async () => {
    fake.consent.mockRejectedValueOnce(new Error("origin does not match"));
    at("auth");
    render(<App />);
    fireEvent.click($("#btn-auth-load")!);
    await waitFor(() => expect(screen.getByText("origin does not match")).toBeTruthy());
  });

  const consent = {
    request: { audience: "https://app.example", response_uri: "https://app.example/cb" },
    metadata: { name: "Example App" },
    headline: "Sign in to Example App",
    context: "Started 12 seconds ago in Chrome on macOS.",
    scopes: [],
  };

  async function openUnlocked() {
    useSession.setState({ identity: IDENTITY, unlocked: true });
    fake.consent.mockResolvedValueOnce(consent);
    at("auth");
    render(<App />);
    fireEvent.click($("#btn-auth-load")!);
    await waitFor(() => expect($("#btn-auth-approve")).toBeTruthy());
  }

  it("finishes a same-device approval in this browser, with nothing in a URL", async () => {
    const went: string[] = [];
    setContinueTo((url) => went.push(url));
    fake.deliver.mockResolvedValueOnce({ delivered: true, resumeUri: "https://app.example/auth/resume?code=c" });
    await openUnlocked();
    fireEvent.click($("#btn-auth-approve")!);
    await waitFor(() => expect($("#btn-auth-continue")).toBeTruthy());
    expect(fake.deliver).toHaveBeenCalledWith(consent.request, "ENCODED", undefined, "");
    expect(went).toEqual(["https://app.example/auth/resume?code=c"]);
    expect($<HTMLAnchorElement>("#btn-auth-continue")!.href).not.toContain("ENCODED");
    expect($("#auth-response")).toBeNull();
  });

  it("sends the code for a cross-device approval and sends nobody anywhere", async () => {
    const went: string[] = [];
    setContinueTo((url) => went.push(url));
    fake.deliver.mockResolvedValueOnce({ delivered: true, resumeUri: "" });
    await openUnlocked();
    expect($("#auth-context")!.textContent).toBe("Started 12 seconds ago in Chrome on macOS.");
    fireEvent.change($("#auth-match")!, { target: { value: "4 2" } });
    fireEvent.click($("#btn-auth-approve")!);
    await waitFor(() => expect($("#auth-result-note")!.textContent).toMatch(/Go back to the screen/));
    expect(fake.deliver).toHaveBeenCalledWith(consent.request, "ENCODED", undefined, "42");
    expect(went).toEqual([]);
    expect($("#btn-auth-continue")).toBeNull();
  });

  it("refuses a malformed code before signing anything", async () => {
    await openUnlocked();
    fireEvent.change($("#auth-match")!, { target: { value: "7" } });
    fireEvent.click($("#btn-auth-approve")!);
    await waitFor(() => expect(screen.getByText(/Enter the 2-digit code/)).toBeTruthy());
    expect(fake.sign).not.toHaveBeenCalled();
  });
});
