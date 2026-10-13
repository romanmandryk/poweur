import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it, vi } from "vitest";

/** Load public/sw.js against a fake worker scope and return its handlers. */
function loadWorker() {
  const handlers: Record<string, (event: any) => void> = {};
  const shown: { title: string; options: any }[] = [];
  const opened: string[] = [];
  const self: any = {
    navigator: { setAppBadge: vi.fn(async () => {}), clearAppBadge: vi.fn(async () => {}) },
    addEventListener: (type: string, fn: (event: any) => void) => (handlers[type] = fn),
    skipWaiting: () => {},
    registration: {
      scope: "https://alice.poweur.net/id/",
      showNotification: async (title: string, options: any) => void shown.push({ title, options }),
    },
    clients: {
      claim: async () => {},
      matchAll: async () => [],
      openWindow: async (url: string) => void opened.push(url),
    },
  };
  new Function("self", readFileSync(join(__dirname, "../../public/sw.js"), "utf8"))(self);
  const run = async (type: string, event: any) => {
    let done: Promise<unknown> = Promise.resolve();
    handlers[type]!({ ...event, waitUntil: (p: Promise<unknown>) => (done = p) });
    await done;
  };
  return { self, run, shown, opened };
}

describe("service worker", () => {
  it("a sign-in push says what it is, keeps its own notification, and opens the request (EPIC-028)", async () => {
    const w = loadWorker();
    await w.run("push", { data: { json: () => ({ v: 1, identity: "alice.poweur.net", kind: "auth", count: 3 }) } });
    expect(w.shown[0]!.title).toBe("Sign-in request waiting · alice.poweur.net");
    expect(w.shown[0]!.options.tag).toBe("poweur:auth:alice.poweur.net");
    expect(w.shown[0]!.options.data.page).toBe("signin");
    // The badge counts mail; a prompt does not change it.
    expect(w.self.navigator.setAppBadge).not.toHaveBeenCalled();

    await w.run("notificationclick", { notification: { close: () => {}, data: w.shown[0]!.options.data } });
    expect(w.opened).toEqual(["https://alice.poweur.net/id/?open=signin"]);
  });

  it("mail still badges and lands in Messages", async () => {
    const w = loadWorker();
    await w.run("push", { data: { json: () => ({ v: 1, identity: "alice.poweur.net", kind: "message", count: 2 }) } });
    expect(w.self.navigator.setAppBadge).toHaveBeenCalledWith(2);
    expect(w.shown[0]!.options.tag).toBe("poweur:alice.poweur.net");
    expect(w.shown[0]!.options.data.page).toBe("messages");
  });
});
