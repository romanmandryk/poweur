import { beforeEach, describe, expect, it } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { App } from "../../src/shell/App";
import { useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { useSession } from "../../src/state/session";
import { saveIdentityRecord } from "../../src/lib/storage.js";
import { resetStores } from "../helpers/stores";

const IDENTITY = "alice.poweur.net";
const signedIn = (unlocked: boolean) => useSession.setState({ identity: IDENTITY, unlocked });
const tab = (container: HTMLElement, page: string) => container.querySelector<HTMLElement>(`.nav-tab[data-page="${page}"]`)!;

beforeEach(resetStores);

describe("shell decision tree (E21-T4)", () => {
  it("a device with no identity gets the generic welcome, with no nav", () => {
    const { container } = render(<App />);
    expect(container.querySelector("#btn-welcome-start")).toBeTruthy();
    expect(container.querySelector(".bottom-nav")).toBeNull();
    expect(container.querySelector(".app-header")).toBeNull();
  });

  it("Get started opens add-id as a full-screen gate, and back returns to the door", () => {
    const { container } = render(<App />);
    fireEvent.click(container.querySelector("#btn-welcome-start")!);
    expect(useRoute.getState().sub).toBe("add-id");
    expect(container.querySelector(".sub-title")!.textContent).toBe("Add identity");
    expect(container.querySelector(".bottom-nav")).toBeNull();

    fireEvent.click(container.querySelector("#btn-back")!);
    expect(container.querySelector("#btn-welcome-start")).toBeTruthy();
  });

  it("a locked identity sees the unlock prompt on destinations that need keys", () => {
    signedIn(false);
    const { container } = render(<App />);
    expect(container.querySelectorAll(".nav-tab")).toHaveLength(5);
    expect(container.querySelector("#btn-unlock-main")!.textContent).toContain("Unlock with passkey");
    expect(container.querySelector(".unlock-name")!.textContent).toBe("alice");

    // Settings and Apps stay reachable while locked.
    fireEvent.click(tab(container, "settings"));
    expect(container.querySelector(".dest-title")!.textContent).toBe("Settings");
    expect(container.querySelector("#btn-unlock-main")).toBeNull();

    fireEvent.click(tab(container, "contacts"));
    expect(container.querySelector("#btn-unlock-main")).toBeTruthy();
  });

  it("the unlock prompt opens the unlock gate", () => {
    saveIdentityRecord(IDENTITY, { identity: IDENTITY, encryptedKeys: { kdf: "prf" } });
    signedIn(false);
    const { container } = render(<App />);
    fireEvent.click(container.querySelector("#btn-unlock-main")!);
    expect(useRoute.getState().sub).toBe("unlock");
    expect(container.querySelector(".bottom-nav")).toBeNull();
  });

  it("switching destinations never recreates the header or nav — the flicker fix", () => {
    signedIn(true);
    const { container } = render(<App />);
    const header = container.querySelector(".app-header");
    const nav = container.querySelector(".bottom-nav");
    const settingsTab = tab(container, "settings");

    for (const [page, title] of [["contacts", "Contacts"], ["files", "Files"], ["launcher", "Apps"], ["settings", "Settings"], ["messages", "Messages"]]) {
      fireEvent.click(tab(container, page));
      expect(container.querySelector(".dest-title")!.textContent).toBe(title);
      expect(tab(container, page).getAttribute("aria-selected")).toBe("true");
    }

    // Same nodes, not look-alikes: a hover or pressed state survives.
    expect(container.querySelector(".app-header")).toBe(header);
    expect(container.querySelector(".bottom-nav")).toBe(nav);
    expect(tab(container, "settings")).toBe(settingsTab);
  });

  it("a data update does not recreate a focused control", () => {
    signedIn(true);
    const { container } = render(<App />);
    const themeButton = container.querySelector<HTMLElement>("#btn-theme")!;
    themeButton.focus();
    act(() => useData.setState({ requests: { ...useData.getState().requests, incoming: [{ sender: "bob.poweur.net", timestamp: "2026-09-01T00:00:00Z" }] } }));
    expect(document.activeElement).toBe(themeButton);
  });

  it("a detail sub-page sits beside the list, keeping the nav", () => {
    signedIn(true);
    const { container } = render(<App />);
    act(() => useRoute.getState().push("new-chat"));
    expect(container.querySelector("#detail-pane .sub-title")!.textContent).toBe("New message");
    expect(container.querySelector("#page-content .dest-title")!.textContent).toBe("Messages");
    expect(container.querySelector(".bottom-nav")).toBeTruthy();
  });

  it("Escape closes a detail sub-page", () => {
    signedIn(true);
    const { container } = render(<App />);
    act(() => useRoute.getState().push("thread"));
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(useRoute.getState().sub).toBeNull();
    expect(container.querySelector("#detail-pane")).toBeNull();
  });

  it("badges count contact requests and clear the label when there are none", () => {
    signedIn(true);
    const { container } = render(<App />);
    expect(tab(container, "contacts").getAttribute("aria-label")).toBe("Contacts");

    act(() =>
      useData.setState({
        requests: {
          ...useData.getState().requests,
          incoming: [
            { sender: "bob.poweur.net", timestamp: "2026-09-01T00:00:00Z" },
            { sender: "carol.poweur.net", timestamp: "2026-09-02T00:00:00Z" },
          ],
        },
      }),
    );
    expect(tab(container, "contacts").getAttribute("aria-label")).toBe("Contacts, 2 new");
    expect(tab(container, "contacts").querySelector(".nav-badge")!.textContent).toBe("2");
  });

  it("the theme toggle flips data-theme and remembers it", () => {
    signedIn(true);
    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: "Toggle theme" }));
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("poweur:theme")).toBe("dark");
    fireEvent.click(screen.getByRole("button", { name: "Toggle theme" }));
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("a launcher host with no identity gets its landing, not the generic welcome", () => {
    useSession.setState({ mode: { mode: "launcher" } });
    const { container } = render(<App />);
    expect(container.querySelector("#btn-welcome-start")).toBeNull();
    expect(container.querySelector(".landing-title")).toBeTruthy();
  });
});
