import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { incomingRequests, incomingShareClaims, incomingShareOffers, navBadges } from "../../src/state/badges";
import { useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { onIdentityTeardown, lockIdentity, switchIdentity, useSession } from "../../src/state/session";
import { closePanel, openPanel, setLoading, toast, useUi } from "../../src/state/ui";
import { LoadingOverlay, PanelHost, Toaster } from "../../src/shell/Overlays";
import { resetStores } from "../helpers/stores";

beforeEach(resetStores);
afterEach(() => vi.useRealTimers());

describe("route store", () => {
  it("go clears the sub-page; push and pop layer one on top", () => {
    const route = useRoute.getState();
    route.push("new-chat", { to: "bob.poweur.net" });
    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: "new-chat", params: { to: "bob.poweur.net" } });
    route.go("files");
    expect(useRoute.getState()).toMatchObject({ page: "files", sub: null, params: {} });
  });

  it("popping a thread forgets the open conversation", () => {
    useData.setState({ thread: { peer: "bob.poweur.net" } });
    useRoute.getState().push("thread");
    useRoute.getState().pop();
    expect(useData.getState().thread).toBeNull();
  });
});

describe("switchIdentity", () => {
  it("resets everything scoped to the previous identity and runs teardowns", () => {
    const stopStream = vi.fn();
    const unregister = onIdentityTeardown(stopStream);
    useSession.setState({ identity: "alice.poweur.net", unlocked: true });
    useData.setState({ messages: [{ id: 1 }], contacts: { ...useData.getState().contacts, list: [{ identity: "x.y" }], loaded: true }, tray: "requests", thread: { peer: "carol.poweur.net" } });

    switchIdentity("bob.poweur.net");

    expect(stopStream).toHaveBeenCalledOnce();
    expect(useSession.getState()).toMatchObject({ identity: "bob.poweur.net", unlocked: false });
    expect(localStorage.getItem("poweur:active")).toBe("bob.poweur.net");
    expect(useData.getState().messages).toEqual([]);
    expect(useData.getState().contacts).toMatchObject({ list: [], loaded: false });
    expect(useData.getState().thread).toBeNull();
    // Not identity data: the tray choice survives, as it did in the legacy app.
    expect(useData.getState().tray).toBe("requests");
    unregister();
  });
});

describe("lockIdentity", () => {
  it("drops keys and identity data but keeps who is selected", () => {
    const stopStream = vi.fn();
    const unregister = onIdentityTeardown(stopStream);
    useSession.setState({ identity: "alice.poweur.net", unlocked: true });
    useData.setState({
      messages: [{ id: 1 }],
      profile: { doc: { display_name: "Alice", bio: "hi" }, explicit: true, loaded: true, loading: false },
      thread: { peer: "bob.poweur.net" },
      tray: "requests",
    });

    lockIdentity();

    expect(stopStream).toHaveBeenCalledOnce();
    expect(useSession.getState()).toMatchObject({ identity: "alice.poweur.net", unlocked: false });
    expect(useData.getState().messages).toEqual([]);
    expect(useData.getState().profile.doc).toBeNull();
    expect(useData.getState().thread).toBeNull();
    expect(useData.getState().tray).toBe("requests");
    unregister();
  });
});

describe("badges", () => {
  const base = () => useData.getState();

  it("count nothing while locked", () => {
    expect(navBadges(base(), "alice.poweur.net", false)).toEqual({});
  });

  it("merge queued and inbox requests, newest per requester, skipping blocked and our own", () => {
    const data = {
      ...base(),
      contacts: { ...base().contacts, list: [{ identity: "mallory.poweur.net", state: "blocked" }] },
      requests: {
        ...base().requests,
        incoming: [
          { sender: "bob.poweur.net", timestamp: "2026-09-01T00:00:00Z", plaintext: "hi" },
          { sender: "bob.poweur.net", timestamp: "2026-09-03T00:00:00Z", plaintext: "again" },
          { sender: "mallory.poweur.net", timestamp: "2026-09-04T00:00:00Z" },
          { sender: "carol.poweur.net", timestamp: "2026-09-02T00:00:00Z", type: "sys.contact.accept" },
        ],
      },
      messages: [
        { type: "sys.contact.request", sender: "dave.poweur.net", timestamp: "2026-09-02T00:00:00Z" },
        { type: "sys.contact.request", sender: "alice.poweur.net", timestamp: "2026-09-05T00:00:00Z" },
      ],
    };
    const requests = incomingRequests(data, "alice.poweur.net");
    expect(requests.map((r) => r.sender)).toEqual(["bob.poweur.net", "dave.poweur.net"]);
    expect(requests[0]).toMatchObject({ intro: "again", queued: true });
  });

  it("count unread anonymous messages on Messages, while the inbox accepts them", () => {
    const anon = { ...base().anon, messages: [{ id: "a", timestamp: "2026-09-01T00:00:00Z" }] };
    const accepting = { ...base().policy, loaded: true, doc: { version: 1, mode: "open", anonymous: { allow: true } } as any };
    expect(navBadges({ ...base(), anon, policy: accepting }, "alice.poweur.net", true).messages).toBe(1);
    // Turned off, there is no tray to clear them from, so they are no count either.
    expect(navBadges({ ...base(), anon }, "alice.poweur.net", true).messages).toBe(0);
  });

  it("finds structurally bound share offers and hides accepted or expired ones", () => {
    const offer = (shareId: string, expires: string) => ({
      id: shareId,
      type: "sys.share.offer",
      sender: "alice.poweur.net",
      recipient: "bob.poweur.net",
      timestamp: "2026-09-23T12:00:00Z",
      expires_at: expires,
      metadata: { share_id: shareId },
      plaintext: JSON.stringify({
        version: 1,
        grant: {
          share_id: shareId,
          owner: "alice.poweur.net",
          path: "shared/project",
          audience: [{ id: "bob.poweur.net" }],
          permissions: ["read", "write"],
        },
      }),
    });
    const data = {
      ...base(),
      requests: {
        ...base().requests,
        incoming: [offer("shr_live", "2126-09-30T00:00:00Z"), offer("shr_mounted", "2126-09-30T00:00:00Z"), offer("shr_old", "2020-01-01T00:00:00Z")],
      },
    };
    const offers = incomingShareOffers(data, "bob.poweur.net", ["shr_mounted"]);
    expect(offers).toHaveLength(1);
    expect(offers[0]).toMatchObject({ shareId: "shr_live", sender: "alice.poweur.net", path: "shared/project", writable: true });
  });

  it("finds share claims only when body, sender, owner and metadata agree", () => {
    const body = {
      version: 1, share_id: "shr_request", owner: "alice.poweur.net",
      token: "aaaaaaaaaaaaaaaaaaaaaaaaaa", claimant: "bob.poweur.net",
      action: "uploaded", claimed_at: "2026-09-24T08:00:00Z",
    };
    const message = {
      id: "claim-1", type: "sys.share.claim", sender: "bob.poweur.net",
      recipient: "alice.poweur.net", timestamp: "2026-09-24T08:00:01Z",
      metadata: { share_id: "shr_request" }, plaintext: JSON.stringify(body),
    };
    const data = { ...base(), requests: { ...base().requests, incoming: [message] } };
    expect(incomingShareClaims(data, "alice.poweur.net")).toMatchObject([
      { sender: "bob.poweur.net", shareId: "shr_request", action: "uploaded" },
    ]);
    expect(incomingShareClaims({ ...data, requests: { ...data.requests, incoming: [{ ...message, sender: "mallory.poweur.net" }] } }, "alice.poweur.net")).toEqual([]);
  });
});

describe("toasts", () => {
  it("show the same message once, then go away", () => {
    vi.useFakeTimers();
    render(<Toaster />);
    act(() => {
      toast("Saved", "success");
      toast("Saved", "success");
      toast("Saved", "error");
    });
    expect(document.querySelectorAll(".toast")).toHaveLength(2);
    expect(document.querySelector(".toast.success")!.textContent).toContain("Saved");

    act(() => vi.advanceTimersByTime(3900));
    expect(document.querySelectorAll(".toast")).toHaveLength(0);
  });
});

describe("loading overlay", () => {
  it("shows the given text while active", () => {
    render(<LoadingOverlay />);
    const root = document.getElementById("loading-root")!;
    expect(root.hidden).toBe(true);
    act(() => setLoading(true, "Creating identity…"));
    expect(root.hidden).toBe(false);
    expect(document.getElementById("loading-text")!.textContent).toBe("Creating identity…");
  });
});

describe("panel", () => {
  it("opens as a dialog, focuses the first field, and closes with its onClose", async () => {
    const onClose = vi.fn();
    render(<PanelHost />);
    act(() => {
      openPanel("Add contact", (close) => (
        <>
          <input aria-label="Identity" />
          <button type="button" onClick={close}>
            Done
          </button>
        </>
      ), onClose);
    });

    const dialog = await screen.findByRole("dialog", { name: "Add contact" });
    expect(dialog.id).toBe("panel-root");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("Identity")));

    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(onClose).toHaveBeenCalledOnce();
    expect(useUi.getState().panel).toBeNull();
  });

  it("puts focus back on the control that opened it", async () => {
    render(
      <>
        <button type="button" id="opener" onClick={() => openPanel("Inbox", () => <input aria-label="Mode" />)}>
          Who can message you
        </button>
        <PanelHost />
      </>,
    );
    const opener = document.getElementById("opener")!;
    opener.focus();
    fireEvent.click(opener);
    await screen.findByRole("dialog", { name: "Inbox" });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("Mode")));

    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(opener));
  });

  it("a stale close cannot shut a newer panel", () => {
    const closeFirst = openPanel("First", () => null);
    openPanel("Second", () => null);
    closeFirst();
    expect(useUi.getState().panel?.title).toBe("Second");
    closePanel();
    expect(useUi.getState().panel).toBeNull();
  });
});
