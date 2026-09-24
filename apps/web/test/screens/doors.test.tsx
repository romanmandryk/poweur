import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

const relay = vi.hoisted(() => ({
  availability: vi.fn(),
  get: vi.fn(),
}));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  identityApiFor: () => relay,
}));
vi.mock("../../src/lib/passkey.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  checkPasskeySupport: vi.fn(() => Promise.resolve({ available: true, prf: true })),
}));

import { resetDoorProbeForTests } from "../../src/actions/door";
import { saveConfig } from "../../src/lib/storage.js";
import { App } from "../../src/shell/App";
import { useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { useSession, type ModeInfo } from "../../src/state/session";
import { resetStores } from "../helpers/stores";

const launcher: ModeInfo = {
  mode: "launcher",
  domain: "poweur.net",
  hostedDomains: ["poweur.net"],
  launcherHost: "id.poweur.net",
  probed: true,
  resolved: true,
  reachable: true,
};

const identityHost = (handle: string): ModeInfo => ({
  ...launcher,
  mode: "identity",
  subject: `${handle}.poweur.net`,
  handle,
});

const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector)!;

beforeEach(() => {
  resetStores();
  resetDoorProbeForTests();
  relay.availability.mockReset();
  relay.get.mockReset();
  saveConfig({ relayUrl: "http://127.0.0.1:8080", parentDomain: "poweur.net" });
  useSession.setState({ config: { relayUrl: "http://127.0.0.1:8080", parentDomain: "poweur.net" } });
  history.replaceState(null, "", "/app/");
});

describe("the launcher landing (E15-T8)", () => {
  it("shows a skeleton until the relay has been asked", () => {
    useSession.setState({ mode: { ...launcher, probed: false } });
    render(<App />);
    expect($("#claim-card-pending")).toBeTruthy();
    expect(document.querySelector("#ni-handle")).toBeNull();
  });

  it("lands on the claim field with the domain as a suffix, and three steps", async () => {
    useSession.setState({ mode: launcher });
    render(<App />);
    expect($(".landing-title").textContent).toBe("Your name. Your inbox. Your files.");
    expect($("#ni-domain-fixed").textContent).toBe(".poweur.net");
    // E15-T10: neither question survives.
    expect(document.querySelector("input#ni-domain")).toBeNull();
    expect(document.querySelectorAll(".landing-step")).toHaveLength(3);
    // No relay prompt: this page is served by its relay.
    expect(document.querySelector("#setup-relay-preset")).toBeNull();
    await waitFor(() => expect(useData.getState().passkey).toEqual({ available: true, prf: true }));
  });

  it("offers a domain picker when the relay hosts several, and a typed domain when none", () => {
    useSession.setState({ mode: { ...launcher, hostedDomains: ["poweur.net", "example.org"] } });
    const { unmount } = render(<App />);
    expect($<HTMLSelectElement>("select#ni-domain").options).toHaveLength(2);
    unmount();

    useSession.setState({ mode: { ...launcher, hostedDomains: [], domain: "" } });
    render(<App />);
    expect($<HTMLInputElement>("input#ni-domain").value).toBe("poweur.net");
  });

  it("judges a name before any passkey is spent", async () => {
    useSession.setState({ mode: launcher });
    relay.availability.mockResolvedValueOnce({
      available: false,
      reason: "reserved",
      message: "This name is reserved by the operator.",
      policy: { min_len: 6, max_len: 24 },
    });
    render(<App />);
    const handle = $<HTMLInputElement>("#ni-handle");
    const claim = $<HTMLButtonElement>("#btn-claim");

    fireEvent.input(handle, { target: { value: "admin" } });
    expect(claim.disabled).toBe(true);
    await waitFor(() => expect($("#ni-availability").textContent).toBe("This name is reserved by the operator."), { timeout: 2000 });
    expect(relay.availability).toHaveBeenCalledWith("admin", "poweur.net");
    expect(claim.disabled).toBe(true);

    relay.availability.mockResolvedValueOnce({ available: true, identity: "melissa.poweur.net", policy: { min_len: 6, max_len: 24 } });
    // A pasted FQDN means the same handle as the bare label.
    fireEvent.input(handle, { target: { value: "Melissa.poweur.net" } });
    expect(handle.value).toBe("melissa");
    await waitFor(() => expect($("#ni-availability").textContent).toBe("melissa.poweur.net is available"), { timeout: 2000 });
    await waitFor(() => expect(claim.disabled).toBe(false));
  });

  it("a link from a sign-in page fills the name, checks it and says where to go back", async () => {
    history.replaceState(null, "", "/app/?handle=Newbie&from=signin");
    useSession.setState({ mode: launcher });
    relay.availability.mockResolvedValue({ available: true, identity: "newbie.poweur.net", policy: null });
    render(<App />);
    expect($<HTMLInputElement>("#ni-handle").value).toBe("newbie");
    expect($("#claim-from-signin").textContent).toContain("go back to that tab");
    await waitFor(() => expect($("#ni-availability").textContent).toBe("newbie.poweur.net is available"), { timeout: 2000 });
    expect(relay.availability).toHaveBeenCalledWith("newbie", "poweur.net");
  });

  it("an ordinary visit has no sign-in note and an empty name", () => {
    history.replaceState(null, "", "/app/?handle=<b>x</b>");
    useSession.setState({ mode: launcher });
    render(<App />);
    expect($<HTMLInputElement>("#ni-handle").value).toBe("");
    expect(document.querySelector("#claim-from-signin")).toBeNull();
  });

  it("refuses up front when this browser has no PRF passkey", async () => {
    useSession.setState({ mode: launcher });
    useData.setState({
      passkey: { available: true, prf: false, reason: "This browser does not support passkeys with PRF." },
      custody: { kind: "passkey", reason: "no_native_keystore" },
    });
    render(<App />);
    expect($("#claim-prf-required").textContent).toContain("does not support passkeys with PRF");
    expect($<HTMLButtonElement>("#btn-claim").disabled).toBe(true);
  });

  it("the claim is poweur.org's closing box: one control with its button", () => {
    useSession.setState({ mode: launcher });
    render(<App />);
    expect($("#claim-card").className).toContain("claim-hero");
    expect($(".claim-title").textContent).toBe("Claim your name on the open internet.");
    expect($(".claim-lede").textContent).toContain("Free hosted IDs on poweur.net.");
    expect($("#btn-claim").closest(".claim-field")).toBe($("#ni-handle").closest(".claim-field"));
    expect($("#btn-claim").textContent).toBe("Claim");
  });

  it("I already have an ID on the launcher opens the ID's own door when it exists", async () => {
    useSession.setState({ mode: launcher });
    relay.availability.mockResolvedValue({ available: false, reason: "taken" });
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign, protocol: "http:", port: "", pathname: "/app/", search: "" });
    try {
      render(<App />);
      // No passkey question here: the keys live on the ID's own origin.
      expect(document.querySelector("#opt-have-id")).toBeNull();
      fireEvent.input($("#have-id-input"), { target: { value: "Bob" } });
      fireEvent.click($("#btn-have-id"));
      await waitFor(() => expect(assign).toHaveBeenCalledWith("http://bob.poweur.net/app/"));
      expect(relay.availability).toHaveBeenCalledWith("bob", "poweur.net");
      expect(useRoute.getState().sub).toBeNull();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("I already have an ID says a missing name doesn't exist and can be claimed above", async () => {
    useSession.setState({ mode: launcher });
    relay.availability.mockResolvedValue({ available: true, identity: "nobody.poweur.net" });
    render(<App />);
    fireEvent.input($("#have-id-input"), { target: { value: "nobody.poweur.net" } });
    fireEvent.keyDown($("#have-id-input"), { key: "Enter" });
    await waitFor(() => expect($("#have-id-error").textContent).toBe("nobody.poweur.net doesn't exist yet. You can claim it above."));
    expect($("#have-id-error").getAttribute("role")).toBe("alert");

    // Free but not claimable (too short, blocked): it still does not exist.
    relay.availability.mockResolvedValue({ available: false, reason: "too_short", message: "Names need at least 6 characters." });
    fireEvent.input($("#have-id-input"), { target: { value: "abc" } });
    expect(document.querySelector("#have-id-error")).toBeNull();
    fireEvent.click($("#btn-have-id"));
    await waitFor(() => expect($("#have-id-error").textContent).toBe("abc.poweur.net doesn't exist."));
  });

  it("I already have an ID does not ask the relay about names it does not host", async () => {
    useSession.setState({ mode: launcher });
    render(<App />);
    fireEvent.input($("#have-id-input"), { target: { value: "carl.example.com" } });
    fireEvent.click($("#btn-have-id"));
    await waitFor(() => expect($("#have-id-error").dataset.state).toBe("elsewhere"));
    expect(relay.availability).not.toHaveBeenCalled();

    fireEvent.input($("#have-id-input"), { target: { value: "" } });
    fireEvent.click($("#btn-have-id"));
    await waitFor(() => expect($("#have-id-error").textContent).toBe("Enter your ID, like alice.poweur.net."));
  });

  it("an unreachable relay is not reported as a missing name", async () => {
    useSession.setState({ mode: launcher });
    relay.availability.mockRejectedValue(new Error("offline"));
    render(<App />);
    fireEvent.input($("#have-id-input"), { target: { value: "bob" } });
    fireEvent.click($("#btn-have-id"));
    await waitFor(() => expect($("#have-id-error").dataset.state).toBe("offline"));
  });

  it("in the shell, I already have an ID is still the add-identity screen", () => {
    useSession.setState({ mode: { ...launcher, mode: "shell" } });
    render(<App />);
    expect(document.querySelector("#have-id-input")).toBeNull();
    fireEvent.click($("#opt-have-id"));
    expect(useRoute.getState().sub).toBe("add-id");
  });

  it("self-hosting links to the guide instead of a DNS form", () => {
    useSession.setState({ mode: launcher });
    render(<App />);
    const link = $<HTMLAnchorElement>("#opt-own-domain");
    expect(link.tagName).toBe("A");
    expect(link.href).toBe("https://poweur.org/docs/relay/self-hosting");
    expect(link.target).toBe("_blank");
    expect(document.querySelector("#ni-provider")).toBeNull();
    expect(document.querySelector("#ni-token")).toBeNull();
  });
});

describe("the identity host's door (E15-T9)", () => {
  it("an unclaimed name offers itself and nothing else", async () => {
    useSession.setState({ mode: identityHost("freename") });
    relay.availability.mockResolvedValue({ available: true, policy: null });
    render(<App />);
    expect(document.querySelector("[role=status]")!.textContent).toBe("Checking this name…");

    await waitFor(() => expect($("#btn-door-claim").textContent).toBe("Claim freename.poweur.net"));
    expect($(".door-name").textContent).toBe("freename");
    expect(relay.availability).toHaveBeenCalledWith("freename", "poweur.net");
    expect(document.querySelector("#ni-handle")).toBeNull();
    expect(document.querySelector("#opt-create-new")).toBeNull();
    // The way out keeps the app it was opened in.
    expect($<HTMLAnchorElement>("#door-launcher-link").href).toBe("http://id.poweur.net/app/");
  });

  it("a reserved name offers no claim at all", async () => {
    useSession.setState({ mode: identityHost("admin") });
    relay.availability.mockResolvedValue({ available: false, reason: "reserved", message: "This name is reserved by the operator." });
    render(<App />);
    await waitFor(() => expect($(".door-message").textContent).toBe("This name is reserved by the operator."));
    expect(document.querySelector("#btn-door-claim")).toBeNull();
  });

  it("a claimed name offers sign-in and adding this device", async () => {
    useSession.setState({ mode: identityHost("bob") });
    relay.availability.mockResolvedValue({ available: false, reason: "taken" });
    render(<App />);
    await waitFor(() => expect($("#btn-door-signin")).toBeTruthy());
    expect($("#opt-join-device").dataset.joinIdentity).toBe("bob.poweur.net");
    expect(document.querySelector("#btn-door-claim")).toBeNull();
    expect(document.querySelector("#signin-id-input")).toBeNull();
  });

  it("an unreachable relay says so and can retry", async () => {
    useSession.setState({ mode: identityHost("bob") });
    relay.availability.mockRejectedValueOnce(new Error("offline"));
    render(<App />);
    await waitFor(() => expect($(".door-message").textContent).toContain("Can't reach the relay"));

    relay.availability.mockResolvedValue({ available: false, reason: "taken" });
    act(() => fireEvent.click(screen.getByRole("button", { name: "Try again" })));
    await waitFor(() => expect($("#btn-door-signin")).toBeTruthy());
  });

  it("outside the hosted domains, asks for the document instead", async () => {
    useSession.setState({ mode: { ...identityHost("carol"), domain: "elsewhere.org" } });
    relay.get.mockRejectedValue(Object.assign(new Error("not found"), { status: 404 }));
    render(<App />);
    await waitFor(() => expect($(".door-message").textContent).toBe("This relay does not host names under this domain."));
    expect(relay.availability).not.toHaveBeenCalled();
  });
});
