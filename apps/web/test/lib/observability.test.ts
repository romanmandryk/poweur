import { afterEach, describe, expect, it, vi } from "vitest";
import { loadConfig, screenName, startObservability } from "../../src/lib/observability";

afterEach(() => {
  document.head.innerHTML = "";
  delete (window as { betterstack?: unknown }).betterstack;
});

describe("observability facade", () => {
  it("names screens without params or hashes", () => {
    expect(screenName("messages", null)).toBe("messages");
    expect(screenName("messages", "thread")).toBe("messages/thread");
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
        ["track", "page-change", { name: "messages" }],
      ]),
    );
  });
});
