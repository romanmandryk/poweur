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

beforeEach(() => {
  resetStores();
  mode.next = { mode: "unknown" };
  at("/app/");
});

describe("boot (E21-T4)", () => {
  it("a blank device starts on Messages with no gate", async () => {
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "messages", sub: null });
    expect(useSession.getState().identity).toBeNull();
  });

  it("a sign-in request routes straight to the approval screen", async () => {
    at("/app/?auth=abc123");
    await boot();
    expect(useRoute.getState()).toMatchObject({ page: "settings", sub: "auth" });
    expect(useData.getState().auth.input).toBe("abc123");
  });

  it("adopts a launcher hand-off, strips the fragment, and asks to unlock", async () => {
    const payload = toBase64url(new TextEncoder().encode(JSON.stringify({
      identity: "alice.poweur.net",
      record: { identity: "alice.poweur.net", encryptedKeys: { kdf: "prf" } },
    })));
    at(`/app/#claim=${payload}`);

    await boot();

    expect(localStorage.getItem("poweur:active")).toBe("alice.poweur.net");
    expect(useSession.getState().identity).toBe("alice.poweur.net");
    expect(useRoute.getState().sub).toBe("unlock");
    expect(location.hash).toBe("");
  });

  it("strips a malformed hand-off without adopting it", async () => {
    at("/app/#claim=not-json");
    await boot();
    expect(location.hash).toBe("");
    expect(useSession.getState().identity).toBeNull();
    expect(useRoute.getState().sub).toBeNull();
  });

  it("a stored identity with no keys and no session asks to unlock", async () => {
    saveIdentityRecord("alice.poweur.net", { identity: "alice.poweur.net" });
    setActiveIdentity("alice.poweur.net");
    await boot();
    expect(useSession.getState().identity).toBe("alice.poweur.net");
    expect(useRoute.getState().sub).toBe("unlock");
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
});
