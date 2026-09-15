import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";

const relay = vi.hoisted(() => ({ resolveForActive: vi.fn(), activeClient: vi.fn(() => null) }));
vi.mock("../../src/actions/relay", () => relay);

import { loadPeerAvatar, resetPeerAvatarsForTests } from "../../src/actions/avatars";
import { forgetAvatar, readLocalAvatar, rememberAvatar, resetAvatarsForTests, saveLocalAvatar } from "../../src/state/avatars";
import { Avatar } from "../../src/ui/Avatar";

const ALICE = "alice.poweur.net";
const BOB = "bob.poweur.net";
const PHOTO = "data:image/jpeg;base64,AAAA";
const img = () => document.querySelector(".id-avatar img");

beforeEach(() => {
  localStorage.clear();
  resetAvatarsForTests();
  resetPeerAvatarsForTests();
  relay.resolveForActive.mockReset();
});

describe("avatar store", () => {
  it("a photo saved on this device shows in that identity's circle, and goes when forgotten", () => {
    render(<Avatar identity={ALICE} />);
    expect(img()).toBeNull();

    act(() => saveLocalAvatar("Alice.Poweur.net", "public/avatars/avatar-1.jpg", PHOTO));
    expect(img()!.getAttribute("src")).toBe(PHOTO);

    act(() => forgetAvatar(ALICE));
    expect(img()).toBeNull();
  });

  it("the device copy is there on first paint, before any key is open", () => {
    localStorage.setItem(`poweur:avatar:${ALICE}`, JSON.stringify({ path: "public/avatars/a.jpg", dataUrl: PHOTO }));
    render(<Avatar identity={ALICE} size="xl" />);
    expect(img()!.getAttribute("src")).toBe(PHOTO);
  });

  it("ignores a stored value that is not an image we encoded", () => {
    localStorage.setItem(`poweur:avatar:${ALICE}`, JSON.stringify({ path: "x", dataUrl: "javascript:alert(1)" }));
    expect(readLocalAvatar(ALICE)).toBeNull();
    render(<Avatar identity={ALICE} />);
    expect(img()).toBeNull();
  });

  it("a resolved profile fills someone else's circle, but never replaces our own copy", () => {
    saveLocalAvatar(ALICE, "public/avatars/mine.jpg", PHOTO);
    act(() => {
      rememberAvatar(ALICE, "https://alice.poweur.net/pub/avatars/stale.jpg");
      rememberAvatar(BOB, "https://bob.poweur.net/pub/avatars/avatar-2.jpg");
    });
    render(
      <>
        <Avatar identity={ALICE} />
        <Avatar identity={BOB} />
      </>,
    );
    const [alice, bob] = document.querySelectorAll(".id-avatar img");
    expect(alice.getAttribute("src")).toBe(PHOTO);
    expect(bob.getAttribute("src")).toBe("https://bob.poweur.net/pub/avatars/avatar-2.jpg");
  });

  it("an explicit src wins, and src={null} means initials", () => {
    saveLocalAvatar(ALICE, "public/avatars/mine.jpg", PHOTO);
    const { rerender } = render(<Avatar identity={ALICE} src="https://example.test/other.jpg" />);
    expect(img()!.getAttribute("src")).toBe("https://example.test/other.jpg");
    rerender(<Avatar identity={ALICE} src={null} />);
    expect(img()).toBeNull();
  });
});

describe("loadPeerAvatar", () => {
  it("resolves someone's public profile once, then leaves the cache to answer", async () => {
    relay.resolveForActive.mockResolvedValue({ avatar: "https://bob.poweur.net/pub/avatars/avatar-2.jpg" });
    render(<Avatar identity={BOB} />);

    await act(async () => {
      await loadPeerAvatar(BOB);
      await loadPeerAvatar(BOB);
    });
    expect(relay.resolveForActive).toHaveBeenCalledTimes(1);
    expect(img()!.getAttribute("src")).toBe("https://bob.poweur.net/pub/avatars/avatar-2.jpg");
  });

  it("an identity that does not resolve keeps its initials", async () => {
    relay.resolveForActive.mockRejectedValue(new Error("offline"));
    render(<Avatar identity={BOB} />);
    await act(async () => loadPeerAvatar(BOB));
    expect(img()).toBeNull();
  });
});
