import { afterEach, describe, expect, it, vi } from "vitest";
import { loadConfig, observabilityConfigUrl, resetObservabilityForTests, sanitizeEventData, screenName, startObservability, syncIdentifiedUser } from "../../src/lib/observability";
import { saveConfig } from "../../src/lib/storage.js";

afterEach(() => {
  document.head.innerHTML = "";
  localStorage.clear();
  resetObservabilityForTests();
  delete (window as { betterstack?: unknown }).betterstack;
  delete (globalThis as { Capacitor?: unknown }).Capacitor;
});

describe("observability facade", () => {
  it("names screens without params or hashes", () => {
    expect(screenName("messages", null)).toBe("messages");
    expect(screenName("messages", "thread")).toBe("messages/thread");
  });

  it("loads observability.json from the document on the web, and from the relay in the shell", () => {
    expect(observabilityConfigUrl()).toBe("observability.json");
    const cap = globalThis as { Capacitor?: { isNativePlatform?: () => boolean } };
    cap.Capacitor = { isNativePlatform: () => true };
    saveConfig({ relayUrl: "https://poweur.net" });
    expect(observabilityConfigUrl()).toBe("https://poweur.net/app/observability.json");
    delete cap.Capacitor;
  });

  it("treats a missing config file as no providers", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false })));
    await expect(loadConfig("observability.json")).resolves.toEqual({ providers: [] });
    vi.unstubAllGlobals();
  });

  it("loads no tag when providers are empty", async () => {
    const obs = await startObservability({ providers: [] });
    obs.pageChange("settings");
    expect(document.querySelector("script[src*='betterstack.net']")).toBeNull();
    expect(window.betterstack).toBeUndefined();
  });

  it("installs the Better Stack tag and records page changes", async () => {
    const obs = await startObservability({
      environment: "production",
      release: "0.1.23",
      providers: [{ type: "betterstack", token: "app_token" }],
    });
    const src = document.querySelector("script[src*='betterstack.net']")?.getAttribute("src");
    expect(src).toContain("app_token");
    obs.pageChange("messages");
    expect(window.betterstack?.q).toEqual(
      expect.arrayContaining([
        ["init", { environment: "production", release: "0.1.23", autoPageview: false }],
        ["track", "app-start", { runtime: "web" }],
        ["track", "page-change", { name: "messages", runtime: "web" }],
      ]),
    );
  });

  it("identifies an unlocked identity and clears on lock", async () => {
    const obs = await startObservability({
      providers: [{ type: "betterstack", token: "app_token" }],
    });
    obs.identify({ id: "alice.poweur.net", username: "alice.poweur.net" });
    expect(window.betterstack?.q).toEqual(
      expect.arrayContaining([
        ["user", { id: "alice.poweur.net", username: "alice.poweur.net", runtime: "web" }],
      ]),
    );
    obs.clearUser();
    expect(window.betterstack?.q).toEqual(expect.arrayContaining([["user", null]]));
  });

  it("syncs the vendor user from session state", async () => {
    await startObservability({
      providers: [{ type: "betterstack", token: "app_token" }],
    });
    syncIdentifiedUser("bob.poweur.net", true);
    expect(window.betterstack?.q).toEqual(
      expect.arrayContaining([["user", expect.objectContaining({ id: "bob.poweur.net" })]]),
    );
    syncIdentifiedUser("bob.poweur.net", false);
    expect(window.betterstack?.q).toEqual(expect.arrayContaining([["user", null]]));
  });

  it("drops message text and counterpart identities from events", () => {
    expect(sanitizeEventData({ kind: "chat", outcome: "sent", plaintext: "secret", peer: "eve.poweur.net" })).toEqual({
      kind: "chat",
      outcome: "sent",
    });
  });

  it("records client errors without identities", async () => {
    const obs = await startObservability({
      providers: [{ type: "betterstack", token: "app_token" }],
    });
    obs.captureError(new Error("send failed"), { source: "react" });
    expect(window.betterstack?.q).toEqual(
      expect.arrayContaining([
        expect.arrayContaining(["track", "error", expect.objectContaining({ name: "Error", message: "send failed", source: "react", runtime: "web" })]),
      ]),
    );
  });
});
