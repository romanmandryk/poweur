import { beforeEach, describe, expect, it, vi } from "vitest";

const mode = vi.hoisted(() => ({ next: { mode: "unknown" } as Record<string, unknown> }));
vi.mock("../../src/lib/mode.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  resolveMode: vi.fn(() => Promise.resolve(mode.next)),
}));

import { boot } from "../../src/shell/boot";
import { toBase64url } from "../../src/lib/vault.js";
import { saveIdentityRecord, setActiveIdentity } from "../../src/lib/storage.js";
import { useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { useSession } from "../../src/state/session";
import { resetStores } from "../helpers/stores";

const at = (url: string) => history.replaceState(null, "", url);

type UrlHandler = (event: { url: string }) => void;

function installAppPlugin(options: { launch?: string; onListen?: (handler: UrlHandler) => void }) {
  const cap = globalThis as { Capacitor?: unknown };
  cap.Capacitor = {
    isNativePlatform: () => true,
    Plugins: {
      App: {
        getLaunchUrl: async () => (options.launch ? { url: options.launch } : undefined),
        addListener: (_event: string, handler: UrlHandler) => {
          options.onListen?.(handler);
          return { remove() {} };
        },
      },
    },
  };
}

beforeEach(() => {
  resetStores();
  mode.next = { mode: "unknown" };
  delete (globalThis as { Capacitor?: unknown }).Capacitor;
  at("/id/");
});

describe("boot (E21-T4)", () => {
  it("a blank device starts on Messages with no gate", async () => {
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });
    expect(useSession.getState().identity).toBeNull();
  });

  it("a sign-in request routes straight to the approval screen", async () => {
    at("/id/?auth=abc123");
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "settings", sub: "auth" });
    expect(useData.getState().auth.input).toBe("abc123");
  });

  it("an approve page from another screen's QR opens a cross-device approval and leaves the address clean (EPIC-028)", async () => {
    at("/id/approve#auth=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2");
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "settings", sub: "auth" });
    expect(useData.getState().auth).toMatchObject({ input: "https://oauth.poweur.org/r/K7QM4XP2", crossDevice: true, noApp: false });
    expect(location.hash).toBe("");
  });

  it("an approve page Android came back to says the app is not installed", async () => {
    at("/id/approve#auth=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2&noapp");
    await boot();
    expect(useData.getState().auth).toMatchObject({ crossDevice: true, noApp: true });
  });

  it("adopts a launcher hand-off, strips the fragment, and asks to unlock", async () => {
    const payload = toBase64url(new TextEncoder().encode(JSON.stringify({
      identity: "alice.poweur.net",
      record: { identity: "alice.poweur.net", encryptedKeys: { kdf: "prf" } },
    })));
    at(`/id/#claim=${payload}`);

    await boot();

    expect(localStorage.getItem("poweur:active")).toBe("alice.poweur.net");
    expect(useSession.getState().identity).toBe("alice.poweur.net");
    expect(useRoute.getState().sub).toBe("unlock");
    expect(location.hash).toBe("");
  });

  it("strips a malformed hand-off without adopting it", async () => {
    at("/id/#claim=not-json");
    await boot();
    expect(location.hash).toBe("");
    expect(useSession.getState().identity).toBeNull();
    expect(useRoute.getState().sub).toBeNull();
  });

  it("does not unlock a stored identity on the public anonymous composer", async () => {
    saveIdentityRecord("alice.poweur.net", { identity: "alice.poweur.net" });
    setActiveIdentity("alice.poweur.net");
    mode.next = { mode: "identity", subject: "bob.poweur.net" };
    at("/id/?anonymous=1");
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });
    expect(useSession.getState().unlocked).toBe(false);
  });

  it("a stored identity with no keys and no session asks to unlock", async () => {
    saveIdentityRecord("alice.poweur.net", { identity: "alice.poweur.net" });
    setActiveIdentity("alice.poweur.net");
    await boot();
    expect(useSession.getState().identity).toBe("alice.poweur.net");
    expect(useRoute.getState().sub).toBe("unlock");
  });

  it("a launcher host stays on the claim page when this origin still has an identity", async () => {
    saveIdentityRecord("alicee.poweur.net", { identity: "alicee.poweur.net" });
    setActiveIdentity("alicee.poweur.net");
    mode.next = { mode: "launcher" };
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });
  });

  it("an installed launcher opens the ID this browser last used, in its own app; a tab stays", async () => {
    document.cookie = "poweur_ids=johnjohn.poweur.net%7Cwork.poweur.net; Path=/";
    const replace = vi.fn();
    try {
      vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("standalone") }));
      vi.stubGlobal("location", { ...window.location, href: "https://poweur.net/id/", protocol: "https:", port: "", pathname: "/id/", search: "", hash: "", replace });
      mode.next = { mode: "launcher", domain: "poweur.net" };
      await boot();
      expect(replace).toHaveBeenCalledWith("https://johnjohn.poweur.net/id/");
    } finally {
      vi.unstubAllGlobals();
      document.cookie = "poweur_ids=; Path=/; Max-Age=0";
    }
  });

  it("a launcher in a browser tab does not redirect, even with known IDs", async () => {
    document.cookie = "poweur_ids=johnjohn.poweur.net; Path=/";
    const replace = vi.fn();
    try {
      vi.stubGlobal("matchMedia", () => ({ matches: false }));
      vi.stubGlobal("location", { ...window.location, href: "https://poweur.net/id/", protocol: "https:", port: "", pathname: "/id/", search: "", hash: "", replace });
      mode.next = { mode: "launcher", domain: "poweur.net" };
      await boot();
      expect(replace).not.toHaveBeenCalled();
      expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });
    } finally {
      vi.unstubAllGlobals();
      document.cookie = "poweur_ids=; Path=/; Max-Age=0";
    }
  });

  it("runs once, and corrects the door and title when the relay answers", async () => {
    mode.next = { mode: "launcher" };
    await boot();
    expect(useSession.getState().mode.mode).toBe("launcher");
    expect(document.title).toBe("Poweur ID — claim your name");
    expect(document.querySelector('meta[name="description"]')!.getAttribute("content")).toContain("Claim an identity");

    mode.next = { mode: "identity", subject: "bob.poweur.net" };
    await boot();
    expect(useSession.getState().mode.mode).toBe("launcher");
  });

  it("a poweur:// launch URL opens approve sign-in with the request filled in", async () => {
    saveIdentityRecord("alice.poweur.net", { identity: "alice.poweur.net" });
    setActiveIdentity("alice.poweur.net");
    mode.next = { mode: "shell" };
    const link = "poweur://auth?request=from-camera";
    installAppPlugin({ launch: link });

    await boot();

    expect(useRoute.getState()).toMatchObject({ page: "settings", sub: "auth" });
    expect(useData.getState().auth.input).toBe("from-camera");
    expect(useData.getState().auth.crossDevice).toBe(true);
  });

  it("a poweur:// open while the app is running prefills approve sign-in", async () => {
    let open: UrlHandler = () => {};
    installAppPlugin({ onListen: (handler) => { open = handler; } });
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });

    open({ url: "poweur://auth?request=from-qr" });

    expect(useRoute.getState()).toMatchObject({ page: "settings", sub: "auth" });
    expect(useData.getState().auth.input).toBe("from-qr");
    expect(useData.getState().auth.crossDevice).toBe(true);
  });

  it("ignores an https link opened into the shell", async () => {
    let open: UrlHandler = () => {};
    installAppPlugin({ onListen: (handler) => { open = handler; } });
    await boot();

    open({ url: "https://alice.poweur.net/id/?auth=abc" });

    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });
    expect(useData.getState().auth.input).toBe("");
  });

  it("a universal link to an approve page opens approve sign-in in the shell (EPIC-028)", async () => {
    let open: UrlHandler = () => {};
    installAppPlugin({ onListen: (handler) => { open = handler; } });
    await boot();
    open({ url: "https://alice.poweur.net/id/approve#auth=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2" });
    expect(useRoute.getState()).toMatchObject({ page: "settings", sub: "auth" });
    expect(useData.getState().auth.input).toBe("https://oauth.poweur.org/r/K7QM4XP2");
    expect(useData.getState().auth.crossDevice).toBe(true);
  });

  it("the shell opened by a pairing QR goes to Add a device, at launch and while running (E11-T8)", async () => {
    const C = "AOEnF9JjCmt3HikT4gFtQDkhhSXV3KzkJ4Vy3xLAuOA";
    const launch = `poweur://pair?pair=K7QM4XP2.${C}&id=bob.poweur.net`;
    let handler: UrlHandler | undefined;
    installAppPlugin({ launch, onListen: (h) => (handler = h) });
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "settings", sub: "pair" });
    expect(useData.getState().pairInput).toBe(launch);

    useRoute.setState({ page: "messages", sub: null, params: {} });
    const again = `poweur://pair?pair=ZZZZ4XP2.${C}&id=bob.poweur.net`;
    handler?.({ url: again });
    expect(useRoute.getState()).toMatchObject({ page: "settings", sub: "pair" });
    expect(useData.getState().pairInput).toBe(again);
  });
});
