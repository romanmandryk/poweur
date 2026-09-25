import { afterEach, describe, expect, it, vi } from "vitest";
import { resetTelemetryForTests, startFaro, telemetry, type ScrubbableItem } from "@poweur/faro";

afterEach(() => {
  resetTelemetryForTests();
  vi.unstubAllGlobals();
});

function start() {
  const sent: ScrubbableItem[] = [];
  const fetchSpy = vi.fn();
  vi.stubGlobal("fetch", fetchSpy);
  history.replaceState(null, "", "/app/?handle=alice#claim=secret");
  const t = startFaro({ url: "/faro/collect", app: "poweur-test", version: "1.0.0", transport: (item) => sent.push(item) });
  return { t, sent, fetchSpy };
}

describe("startFaro", () => {
  it("is a no-op without a collector URL", () => {
    const t = startFaro({ url: "", app: "poweur-test" });
    t.pageView("messages");
    expect(telemetry()).toBe(t);
    expect(t.identified).toBe(false);
  });

  it("anonymous by default: screen names, scrubbed errors, no user, nothing stored", async () => {
    const { t, sent } = start();
    t.pageView("messages/thread");
    t.error(new Error("Could not restore alice.poweur.net"));
    await vi.waitFor(() => expect(sent.length).toBeGreaterThanOrEqual(2));
    const text = JSON.stringify(sent);
    expect(text).toContain("page_view");
    expect(text).toContain("messages/thread");
    expect(text).toContain("Could not restore <id>");
    expect(text).not.toMatch(/alice|secret|handle=/);
    for (const item of sent) expect(item.meta?.user).toBeUndefined();
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("carries the ID only after the user opts in, and stops when they opt out", async () => {
    const { t, sent } = start();
    t.setIdentity("alice.poweur.net");
    expect(t.identified).toBe(true);
    t.event("files", { kind: "upload" });
    await vi.waitFor(() => expect(sent.some((item) => (item.meta?.user as any)?.id === "alice.poweur.net")).toBe(true));

    sent.length = 0;
    t.setIdentity(null);
    t.error(new Error("after opt-out alice.poweur.net"));
    await vi.waitFor(() => expect(sent.length).toBeGreaterThan(0));
    expect(JSON.stringify(sent)).not.toContain("alice");
  });
});
