import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../src/lib/native.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  hasNativeKeystore: () => true,
  wrapKeysNative: vi.fn(async () => ({ kdf: "native" })),
  biometricAvailability: vi.fn(async () => ({ available: true, kind: "face" })),
}));
vi.mock("../../src/lib/keystore.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  enrollThisBrowser: vi.fn(() => Promise.resolve()),
}));
vi.mock("../../src/lib/mode.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  resolveMode: vi.fn(async () => ({ mode: "shell" })),
}));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => ({ sessions: { ensure: vi.fn(() => Promise.resolve({})) }, signer: {} }),
  identityApiFor: () => ({}),
}));
vi.mock("@poweur/client", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  createIdentity: vi.fn(async (_api: unknown, identity: string) => ({
    document: { identity, updated_at: "2026-09-16T00:00:00Z" },
  })),
}));

import { createIdentity } from "../../src/actions/identity";
import { saveIdentityRecord, setUnlockedKeys } from "../../src/lib/storage.js";
import { useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { useSession } from "../../src/state/session";
import { resetStores } from "../helpers/stores";

beforeEach(() => {
  resetStores();
  localStorage.setItem("poweur:config", JSON.stringify({ relayUrl: "http://127.0.0.1:8080" }));
  useSession.setState({ config: { relayUrl: "http://127.0.0.1:8080" } });
});

describe("createIdentity", () => {
  it("signs out the previous identity so onboarding cannot inherit its profile or inbox", async () => {
    saveIdentityRecord("john.poweur.net", { identity: "john.poweur.net", encryptedKeys: { kdf: "native" } });
    localStorage.setItem("poweur:active", "john.poweur.net");
    setUnlockedKeys("john.poweur.net", { kty: "OKP" }, { kty: "OKP" });
    useSession.setState({ identity: "john.poweur.net", unlocked: true });
    useData.setState({
      messages: [{ id: "m1", plaintext: "john's inbox" }],
      profile: { doc: { display_name: "John", bio: "hello" }, explicit: true, loaded: true, loading: false },
    });

    await createIdentity({ handle: "dddddd", domain: "poweur.net", hosted: true });

    expect(useSession.getState()).toMatchObject({ identity: "dddddd.poweur.net", unlocked: true });
    expect(useData.getState().messages).toEqual([]);
    expect(useData.getState().profile.doc).toBeNull();
    expect(useData.getState().onboard).toEqual({ step: 1 });
    expect(useRoute.getState().sub).toBe("onboarding");
  });
});
