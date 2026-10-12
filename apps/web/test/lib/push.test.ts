import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Notifications, badge and install (EPIC-009 E09-T12). The browser APIs are
// stubbed per test; what is asserted is the state machine the Settings row
// shows and the calls that reach the relay.

const IPHONE = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1";
const PIXEL = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 Chrome/140.0 Mobile Safari/537.36";
const KEY = "BP6udfvJ0M1y2KgP71cvyKWCJeURCvNNAgI-QqnwuoXkE4CGBKstEedou_ytkWjHx8tpirLd3-wYddGnJAaDi4Q";

function fakeClient(enabled = true) {
  const calls: string[] = [];
  const subscribed: unknown[] = [];
  const client = {
    push: () => ({
      config: async () => (enabled ? { enabled: true, vapid_public_key: KEY } : { enabled: false }),
      subscribe: async (sub: unknown) => {
        calls.push("subscribe");
        subscribed.push(sub);
        return { enabled: true };
      },
      unsubscribe: async () => {
        calls.push("unsubscribe");
        return { enabled: false };
      },
      test: async () => {
        calls.push("test");
        return { sent: true };
      },
    }),
  };
  return { client, calls, subscribed };
}

function stubBrowser({ ua = PIXEL, standalone = false, permission = "default" as NotificationPermission } = {}) {
  let current: any = null;
  const pushManager = {
    getSubscription: vi.fn(async () => current),
    subscribe: vi.fn(async (options: any) => {
      current = {
        endpoint: "https://fcm.googleapis.com/fcm/send/abc",
        options,
        unsubscribe: vi.fn(async () => true),
        toJSON: () => ({ endpoint: "https://fcm.googleapis.com/fcm/send/abc", keys: { p256dh: "p", auth: "a" } }),
      };
      return current;
    }),
  };
  const registration = { pushManager };
  const notification = Object.assign(function Notification() {}, {
    permission,
    requestPermission: vi.fn(async () => {
      notification.permission = "granted";
      return "granted";
    }),
  });
  vi.stubGlobal("Notification", notification);
  vi.stubGlobal("PushManager", function PushManager() {});
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: standalone && query.includes("standalone") }));
  const setAppBadge = vi.fn(async () => {});
  const clearAppBadge = vi.fn(async () => {});
  Object.defineProperty(globalThis.navigator, "userAgent", { value: ua, configurable: true });
  Object.defineProperty(globalThis.navigator, "maxTouchPoints", { value: 0, configurable: true });
  Object.defineProperty(globalThis.navigator, "serviceWorker", {
    value: { register: vi.fn(async () => registration), ready: Promise.resolve(registration), addEventListener: vi.fn() },
    configurable: true,
  });
  Object.defineProperty(globalThis.navigator, "setAppBadge", { value: setAppBadge, configurable: true });
  Object.defineProperty(globalThis.navigator, "clearAppBadge", { value: clearAppBadge, configurable: true });
  return { pushManager, notification, setAppBadge, clearAppBadge };
}

const load = () => import("../../src/lib/push");

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("platform detection", () => {
  it("recognises iPhone and iPad-as-Mac, not Android", async () => {
    const { isIOS } = await load();
    expect(isIOS(IPHONE, 5)).toBe(true);
    expect(isIOS("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) Safari/605", 5)).toBe(true);
    expect(isIOS("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) Safari/605", 0)).toBe(false);
    expect(isIOS(PIXEL, 5)).toBe(false);
  });

  it("decodes a base64url VAPID key to the 65-byte point", async () => {
    const { urlBase64ToBytes } = await load();
    const bytes = urlBase64ToBytes(KEY);
    expect(bytes).toHaveLength(65);
    expect(bytes[0]).toBe(4);
  });
});

describe("pushState", () => {
  it("asks iOS Safari to install first, but not the installed app", async () => {
    stubBrowser({ ua: IPHONE });
    let push = await load();
    expect(await push.pushState(fakeClient().client, "alice.poweur.net")).toBe("needs-install");

    vi.resetModules();
    stubBrowser({ ua: IPHONE, standalone: true });
    push = await load();
    expect(await push.pushState(fakeClient().client, "alice.poweur.net")).toBe("off");
  });

  it("reports a relay without push, and blocked permission", async () => {
    stubBrowser();
    const push = await load();
    expect(await push.pushState(fakeClient(false).client, "alice.poweur.net")).toBe("relay-disabled");
    vi.resetModules();
    stubBrowser({ permission: "denied" });
    expect(await (await load()).pushState(fakeClient().client, "alice.poweur.net")).toBe("denied");
  });

  it("is unsupported without the Push API", async () => {
    stubBrowser();
    vi.stubGlobal("PushManager", undefined);
    const push = await load();
    expect(await push.pushState(fakeClient().client, "alice.poweur.net")).toBe("unsupported");
  });
});

describe("enable, test, disable", () => {
  it("subscribes with the relay key and registers the device", async () => {
    const browser = stubBrowser();
    const push = await load();
    const { client, calls, subscribed } = fakeClient();

    await push.enablePush(client, "alice.poweur.net");
    expect(browser.notification.requestPermission).toHaveBeenCalled();
    const options = browser.pushManager.subscribe.mock.calls[0][0] as any;
    expect(options.userVisibleOnly).toBe(true);
    expect(options.applicationServerKey).toHaveLength(65);
    expect(subscribed).toEqual([{ endpoint: "https://fcm.googleapis.com/fcm/send/abc", keys: { p256dh: "p", auth: "a" } }]);
    expect(await push.pushState(client, "alice.poweur.net")).toBe("on");
    // Another identity in the same origin has not turned it on.
    expect(await push.pushState(client, "bob.poweur.net")).toBe("off");

    await push.testPush(client);
    await push.disablePush(client, "alice.poweur.net");
    expect(calls).toEqual(["subscribe", "test", "unsubscribe"]);
    expect(await push.pushState(client, "alice.poweur.net")).toBe("off");
  });

  it("refuses with a readable reason when permission is not granted", async () => {
    const browser = stubBrowser();
    browser.notification.requestPermission.mockResolvedValueOnce("denied");
    const push = await load();
    const { client, calls } = fakeClient();
    await expect(push.enablePush(client, "alice.poweur.net")).rejects.toThrow(/blocked/);
    expect(calls).toEqual([]);
  });

  it("refuses on iOS Safari before asking for anything", async () => {
    const browser = stubBrowser({ ua: IPHONE });
    const push = await load();
    await expect(push.enablePush(fakeClient().client, "alice.poweur.net")).rejects.toThrow(/Home Screen/);
    expect(browser.notification.requestPermission).not.toHaveBeenCalled();
  });
});

describe("syncAppBadge", () => {
  it("sets a count and clears at zero", async () => {
    const browser = stubBrowser();
    const push = await load();
    push.syncAppBadge(3);
    push.syncAppBadge(0);
    expect(browser.setAppBadge).toHaveBeenCalledWith(3);
    expect(browser.clearAppBadge).toHaveBeenCalled();
  });
});

describe("installMode", () => {
  it("is installed in standalone, ios in Safari, manual elsewhere", async () => {
    stubBrowser({ standalone: true });
    expect((await load()).installMode()).toBe("installed");
    vi.resetModules();
    stubBrowser({ ua: IPHONE });
    expect((await load()).installMode()).toBe("ios");
    vi.resetModules();
    stubBrowser();
    expect((await load()).installMode()).toBe("manual");
  });
});
