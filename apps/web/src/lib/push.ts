/**
 * Notifications, icon badge and install for the web app (EPIC-009 E09-T12).
 *
 * Each hosted identity's `/app/` installs as its own app (the relay names the
 * manifest after the host). This module registers the service worker
 * (`public/sw.js`), subscribes this device to the relay's background push
 * (E09-T11), keeps the home-screen badge in step with what is unread, and
 * offers the browser's install prompt where there is one.
 *
 * The native shell has its own transports (EPIC-019 E19-T4) and never
 * registers a service worker: everything here is a no-op inside it.
 */
import { isShellRuntime } from "./storage.js";

export type PushState =
  /** No Push API (old browser, private mode, the native shell). */
  | "unsupported"
  /** iOS/iPadOS: push exists only for apps added to the Home Screen. */
  | "needs-install"
  /** The relay does not send background push. */
  | "relay-disabled"
  /** The user blocked notifications for this site. */
  | "denied"
  | "off"
  | "on";

const PUSH_KEY = (identity: string) => `poweur.push.${identity.toLowerCase()}`;

type Nav = Navigator & {
  standalone?: boolean;
  setAppBadge?: (count?: number) => Promise<void>;
  clearAppBadge?: () => Promise<void>;
};

const nav = (): Nav | undefined => globalThis.navigator as Nav | undefined;

/** iPhone, iPod, or an iPad (which reports itself as a Mac with touch). */
export function isIOS(userAgent = nav()?.userAgent ?? "", touchPoints = nav()?.maxTouchPoints ?? 0): boolean {
  return /iPhone|iPad|iPod/.test(userAgent) || (/Macintosh/.test(userAgent) && touchPoints > 1);
}

/** Running as an installed app rather than a browser tab. */
export function isStandalone(): boolean {
  try {
    if (nav()?.standalone) return true;
    return Boolean(globalThis.matchMedia?.("(display-mode: standalone)").matches);
  } catch {
    return false;
  }
}

/** Whether this browser can do Web Push at all, before asking the relay. */
export function pushCapability(): "supported" | "unsupported" | "needs-install" {
  if (isShellRuntime()) return "unsupported";
  const supported =
    typeof globalThis.Notification === "function" &&
    "serviceWorker" in (nav() ?? {}) &&
    typeof (globalThis as any).PushManager === "function";
  if (isIOS() && !isStandalone()) return "needs-install";
  return supported ? "supported" : "unsupported";
}

let registration: Promise<ServiceWorkerRegistration | null> | null = null;

/** Register `sw.js` once. Resolves null where there is no service worker. */
export function registerServiceWorker(): Promise<ServiceWorkerRegistration | null> {
  if (registration) return registration;
  const sw = nav()?.serviceWorker;
  if (isShellRuntime() || !sw) return (registration = Promise.resolve(null));
  registration = sw.register("./sw.js").catch(() => null);
  return registration;
}

export function urlBase64ToBytes(base64url: string): Uint8Array<ArrayBuffer> {
  const padded = base64url.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(base64url.length / 4) * 4, "=");
  const binary = atob(padded);
  const out = new Uint8Array(new ArrayBuffer(binary.length));
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

const sameKey = (current: ArrayBuffer | null | undefined, wanted: Uint8Array) => {
  if (!current) return false;
  const bytes = new Uint8Array(current);
  return bytes.length === wanted.length && bytes.every((b, i) => b === wanted[i]);
};

function remembered(identity: string): string {
  try {
    return localStorage.getItem(PUSH_KEY(identity)) ?? "";
  } catch {
    return "";
  }
}

function remember(identity: string, endpoint: string | null) {
  try {
    if (endpoint) localStorage.setItem(PUSH_KEY(identity), endpoint);
    else localStorage.removeItem(PUSH_KEY(identity));
  } catch {
    // Without storage the state just reads as "off" next time.
  }
}

/** The client surface this module needs: `PoweurClient.push()`. */
export interface PushClient {
  push(): {
    config(): Promise<{ enabled: boolean; vapid_public_key?: string }>;
    subscribe(subscription: { endpoint: string; keys: { p256dh: string; auth: string } }): Promise<unknown>;
    unsubscribe(): Promise<unknown>;
    test(): Promise<{ sent: boolean }>;
  };
}

/** Where notifications stand for identity on this device. */
export async function pushState(client: PushClient, identity: string): Promise<PushState> {
  const capability = pushCapability();
  if (capability !== "supported") return capability;
  const config = await client.push().config().catch(() => ({ enabled: false }));
  if (!config.enabled) return "relay-disabled";
  if (Notification.permission === "denied") return "denied";
  if (Notification.permission !== "granted" || !remembered(identity)) return "off";
  const reg = await registerServiceWorker();
  const subscription = await reg?.pushManager.getSubscription();
  return subscription && subscription.endpoint === remembered(identity) ? "on" : "off";
}

/**
 * Ask for permission (must run from a tap), subscribe with the relay's key
 * and register this device. Throws with a sentence the UI can show.
 */
export async function enablePush(client: PushClient, identity: string): Promise<void> {
  const capability = pushCapability();
  if (capability === "needs-install") throw new Error("On iPhone and iPad, add this app to your Home Screen first.");
  if (capability !== "supported") throw new Error("This browser cannot show notifications.");
  const config = await client.push().config();
  if (!config.enabled || !config.vapid_public_key) throw new Error("Your relay does not send notifications yet.");
  const permission = await Notification.requestPermission();
  if (permission !== "granted") throw new Error("Notifications are blocked for this site. Allow them in your browser settings.");
  const reg = await registerServiceWorker();
  if (!reg) throw new Error("This browser cannot show notifications.");
  await navigator.serviceWorker.ready;
  const key = urlBase64ToBytes(config.vapid_public_key);
  let subscription = await reg.pushManager.getSubscription();
  // A subscription made for another key (the relay rotated it) cannot be used.
  if (subscription && !sameKey(subscription.options?.applicationServerKey, key)) {
    await subscription.unsubscribe().catch(() => false);
    subscription = null;
  }
  subscription ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  const json = subscription.toJSON();
  if (!json.endpoint || !json.keys?.p256dh || !json.keys?.auth) throw new Error("The browser returned an incomplete subscription.");
  await client.push().subscribe({ endpoint: json.endpoint, keys: { p256dh: json.keys.p256dh, auth: json.keys.auth } });
  remember(identity, json.endpoint);
}

/**
 * Stop notifications for identity on this device. The browser subscription
 * stays: another identity in this origin may still use it.
 */
export async function disablePush(client: PushClient, identity: string): Promise<void> {
  await client.push().unsubscribe();
  remember(identity, null);
}

export async function testPush(client: PushClient): Promise<void> {
  await client.push().test();
}

/** Keep the home-screen badge equal to what the app shows as unread. */
export function syncAppBadge(count: number): void {
  const n = nav();
  if (!n?.setAppBadge || isShellRuntime()) return;
  const done = count > 0 ? n.setAppBadge(count) : (n.clearAppBadge?.() ?? n.setAppBadge(0));
  void done.catch(() => {});
}

// ─── Install ─────────────────────────────────────────────────────────────────

type InstallPrompt = Event & { prompt: () => Promise<void>; userChoice: Promise<{ outcome: string }> };
let deferredInstall: InstallPrompt | null = null;
const installListeners = new Set<() => void>();

/** Whether the browser offered an install prompt we can show. */
export const canPromptInstall = () => deferredInstall !== null;

export function onInstallAvailable(listener: () => void): () => void {
  installListeners.add(listener);
  return () => installListeners.delete(listener);
}

/** Show the browser's install dialog; true when the user accepted. */
export async function promptInstall(): Promise<boolean> {
  const event = deferredInstall;
  if (!event) return false;
  deferredInstall = null;
  await event.prompt();
  const choice = await event.userChoice.catch(() => ({ outcome: "dismissed" }));
  installListeners.forEach((listener) => listener());
  return choice.outcome === "accepted";
}

/** How this device installs the app, for the Settings row. */
export function installMode(): "installed" | "prompt" | "ios" | "manual" | "none" {
  if (isShellRuntime()) return "none";
  if (isStandalone()) return "installed";
  if (deferredInstall) return "prompt";
  if (isIOS()) return "ios";
  return "manual";
}

// ─── Wiring ──────────────────────────────────────────────────────────────────

/**
 * Register the worker, capture the install prompt and route messages from
 * the worker. `onOpen` moves the app to a destination (a notification tap);
 * `onPush` refreshes data while a page is open.
 */
export function installPushClient(handlers: { onOpen: (page: string) => void; onPush: () => void }): void {
  if (isShellRuntime() || typeof window === "undefined") return;
  window.addEventListener("beforeinstallprompt", (event) => {
    event.preventDefault();
    deferredInstall = event as InstallPrompt;
    installListeners.forEach((listener) => listener());
  });
  window.addEventListener("appinstalled", () => {
    deferredInstall = null;
    installListeners.forEach((listener) => listener());
  });
  const sw = nav()?.serviceWorker;
  if (!sw) return;
  void registerServiceWorker();
  sw.addEventListener("message", (event: MessageEvent) => {
    const data = event.data ?? {};
    if (data.type === "poweur:open" && typeof data.page === "string") handlers.onOpen(data.page);
    if (data.type === "poweur:push") handlers.onPush();
  });
  // A tap that had to open a new window says where in the URL.
  const open = new URL(globalThis.location.href).searchParams.get("open");
  if (open) {
    const url = new URL(globalThis.location.href);
    url.searchParams.delete("open");
    history.replaceState(history.state, "", url.pathname + url.search + url.hash);
    handlers.onOpen(open);
  }
}
