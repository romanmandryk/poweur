import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";

const fake = vi.hoisted(() => {
  const enroll = {
    offer: vi.fn(() =>
      Promise.resolve({ rendezvousId: "rv-123-abc", sas: "482913", expiresAt: new Date(Date.now() + 600_000).toISOString() }),
    ),
    claim: vi.fn(),
    cancel: vi.fn(() => Promise.resolve()),
  };
  return {
    enroll,
    poll: { stop: vi.fn(), checkNow: vi.fn(), options: null as any },
    adopt: vi.fn(() => Promise.resolve()),
  };
});
vi.mock("../../src/actions/identity", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  enrollApiForJoin: vi.fn(() => Promise.resolve({ enroll: fake.enroll, relayUrl: "http://127.0.0.1:8080" })),
  adoptIdentity: fake.adopt,
}));
vi.mock("../../src/lib/enroll-wait.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  startJoinPoll: vi.fn((options: unknown) => {
    fake.poll.options = options;
    return fake.poll;
  }),
}));

import { enrollApiForJoin } from "../../src/actions/identity";
import { openJoinDevicePanel } from "../../src/screens/JoinDevice";
import { PanelHost } from "../../src/shell/Overlays";
import { useSession } from "../../src/state/session";
import { closePanel } from "../../src/state/ui";
import { resetStores } from "../helpers/stores";

const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);

beforeEach(() => {
  resetStores();
  vi.clearAllMocks();
  fake.poll.options = null;
});

describe("Add this device (E11-T3)", () => {
  it("on an identity host, shows the code at once without asking who you are", async () => {
    render(<PanelHost />);
    act(() => openJoinDevicePanel("bob.poweur.net"));

    await waitFor(() => expect($(".rendezvous-code")!.textContent).toBe("rv-123-abc"));
    expect($(".sas-code")!.textContent).toBe("482913");
    expect($("#panel-root")!.textContent).toContain("bob.poweur.net");
    expect($("#join-identity")).toBeNull();
    expect($("#btn-join-start")).toBeNull();
    expect(enrollApiForJoin).toHaveBeenCalledWith("bob.poweur.net");
    expect(fake.enroll.offer).toHaveBeenCalledWith("bob.poweur.net", expect.any(String));
  });

  it("closing the panel frees the rendezvous", async () => {
    render(<PanelHost />);
    act(() => openJoinDevicePanel("bob.poweur.net"));
    await waitFor(() => expect($(".rendezvous-code")).toBeTruthy());

    act(() => closePanel());
    expect(fake.poll.stop).toHaveBeenCalled();
    expect(fake.enroll.cancel).toHaveBeenCalledWith("bob.poweur.net", expect.objectContaining({ rendezvousId: "rv-123-abc" }));
  });

  it("elsewhere, completes a bare handle with the host's domain", async () => {
    useSession.setState({ mode: { mode: "unknown", domain: "poweur.net", hostedDomains: ["poweur.net"] } });
    render(<PanelHost />);
    act(() => openJoinDevicePanel());

    fireEvent.change($("#join-identity")!, { target: { value: "Alice" } });
    fireEvent.click($("#btn-join-start")!);
    await waitFor(() => expect($(".sas-code")).toBeTruthy());
    expect(fake.enroll.offer).toHaveBeenCalledWith("alice.poweur.net", expect.any(String));
  });

  it("an approval adopts the identity from the seed and closes the panel", async () => {
    render(<PanelHost />);
    act(() => openJoinDevicePanel("bob.poweur.net"));
    await waitFor(() => expect(fake.poll.options).toBeTruthy());

    await act(() => fake.poll.options.onSeed(new Uint8Array(32).fill(3)));
    expect($("#panel-root")).toBeNull();
    // Approved, so there is no rendezvous left to cancel.
    expect(fake.enroll.cancel).not.toHaveBeenCalled();
    expect(fake.adopt).toHaveBeenCalledWith(
      expect.objectContaining({ identity: "bob.poweur.net", relayUrl: "http://127.0.0.1:8080", signingJWK: expect.any(Object) }),
    );
  });

  it("a terminal error offers to try again", async () => {
    render(<PanelHost />);
    act(() => openJoinDevicePanel("bob.poweur.net"));
    await waitFor(() => expect(fake.poll.options).toBeTruthy());

    act(() => fake.poll.options.onError(new Error("rendezvous expired"), { terminal: true }));
    expect($("#btn-join-start")!.textContent).toBe("Try again");
  });
});
