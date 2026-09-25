import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const faro = vi.hoisted(() => {
  const t = { pageView: vi.fn(), event: vi.fn(), error: vi.fn(), setIdentity: vi.fn(), identified: false };
  return { t, started: false };
});
vi.mock("@poweur/faro", () => ({
  startFaro: vi.fn(() => {
    faro.started = true;
    return faro.t;
  }),
  // Before start, telemetry() is Faro's no-op; the facade never needs to know.
  telemetry: () => faro.t,
  resetTelemetryForTests: vi.fn(),
}));

import { startFaro } from "@poweur/faro";
import {
  collectorUrl,
  getObservability,
  loadConfig,
  observabilityConfigUrl,
  resetObservabilityForTests,
  sanitizeEventData,
  screenName,
  setAnalyticsConsent,
  startObservability,
  syncIdentifiedUser,
  trackAction,
} from "../../src/lib/observability";
import { saveConfig } from "../../src/lib/storage.js";
import { APP_VERSION } from "../../src/build-info";

beforeEach(() => {
  vi.clearAllMocks();
  faro.started = false;
});
afterEach(() => {
  localStorage.clear();
  resetObservabilityForTests();
  delete (globalThis as { Capacitor?: unknown }).Capacitor;
  vi.unstubAllGlobals();
});

const shell = () => {
  (globalThis as { Capacitor?: unknown }).Capacitor = { isNativePlatform: () => true };
  saveConfig({ relayUrl: "https://poweur.net" });
};

describe("observability facade", () => {
  it("names screens without params or hashes", () => {
    expect(screenName("messages", null)).toBe("messages");
    expect(screenName("messages", "thread")).toBe("messages/thread");
  });

  it("loads observability.json from the document on the web, and from the relay in the shell", () => {
    expect(observabilityConfigUrl()).toBe("observability.json");
    shell();
    expect(observabilityConfigUrl()).toBe("https://poweur.net/app/observability.json");
  });

  it("sends to the page's own origin on the web, and to the relay from the shell", () => {
    expect(collectorUrl({ type: "faro", url: "/faro/collect" })).toBe("/faro/collect");
    shell();
    expect(collectorUrl({ type: "faro", url: "/faro/collect" })).toBe("https://poweur.net/faro/collect");
    expect(collectorUrl({ type: "faro", url: "https://collect.example/faro" })).toBe("https://collect.example/faro");
  });

  it("treats a missing config file as no providers", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false })));
    await expect(loadConfig("observability.json")).resolves.toEqual({ providers: [] });
  });

  it("starts nothing without a Faro provider", async () => {
    await startObservability({ providers: [] });
    await startObservability({ providers: [{ type: "betterstack" }] });
    expect(startFaro).not.toHaveBeenCalled();
  });

  it("starts the anonymous tier with a Faro provider", async () => {
    await startObservability({ environment: "production", release: "0.1.40", providers: [{ type: "faro", url: "/faro/collect" }] });
    expect(startFaro).toHaveBeenCalledWith({ url: "/faro/collect", app: "poweur-web", version: APP_VERSION, environment: "production" });
    expect(faro.t.event).toHaveBeenCalledWith("app-start", { runtime: "web" });
    expect(faro.t.setIdentity).not.toHaveBeenCalledWith(expect.any(String));
  });

  it("routes actions, screens and errors through, with free-form content dropped", async () => {
    await startObservability({ providers: [{ type: "faro", url: "/faro/collect" }] });
    trackAction("messages", { kind: "chat", plaintext: "secret", peer: "eve.poweur.net" });
    expect(faro.t.event).toHaveBeenCalledWith("messages", { runtime: "web", kind: "chat" });
    getObservability().pageChange("settings");
    expect(faro.t.pageView).toHaveBeenCalledWith("settings");
    const error = new Error("send failed");
    getObservability().captureError(error, { source: "react", token: "abc" });
    expect(faro.t.error).toHaveBeenCalledWith(error, { runtime: "web", source: "react" });
  });

  it("attaches the ID only after opting in, and drops it on lock, switch or opt-out", async () => {
    await startObservability({ providers: [{ type: "faro", url: "/faro/collect" }] });
    setAnalyticsConsent("alice.poweur.net", true);
    expect(faro.t.setIdentity).toHaveBeenLastCalledWith("alice.poweur.net");

    syncIdentifiedUser("alice.poweur.net", true); // same identity, still unlocked: kept
    expect(faro.t.setIdentity).toHaveBeenLastCalledWith("alice.poweur.net");

    syncIdentifiedUser("alice.poweur.net", false); // locked
    expect(faro.t.setIdentity).toHaveBeenLastCalledWith(null);

    setAnalyticsConsent("alice.poweur.net", true);
    syncIdentifiedUser("bob.poweur.net", true); // another identity, whose choice is not known yet
    expect(faro.t.setIdentity).toHaveBeenLastCalledWith(null);

    setAnalyticsConsent("bob.poweur.net", false);
    expect(faro.t.setIdentity).toHaveBeenLastCalledWith(null);
  });

  it("an opt-in that arrives before the config loads still applies", async () => {
    setAnalyticsConsent("alice.poweur.net", true);
    vi.mocked(faro.t.setIdentity).mockClear();
    await startObservability({ providers: [{ type: "faro", url: "/faro/collect" }] });
    expect(faro.t.setIdentity).toHaveBeenCalledWith("alice.poweur.net");
  });

  it("drops message text and counterpart identities from events", () => {
    expect(sanitizeEventData({ kind: "chat", outcome: "sent", plaintext: "secret", peer: "eve.poweur.net" })).toEqual({
      kind: "chat",
      outcome: "sent",
    });
  });
});
