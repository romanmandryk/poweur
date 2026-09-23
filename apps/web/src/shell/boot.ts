/**
 * Boot: decide the first screen before the first paint, then correct the front
 * door when the relay says which host this is (from app.js `boot()`).
 */
import { isSessionValid } from "@poweur/client";
import {
  getActiveIdentity,
  getUnlockedKeys,
  isShellRuntime,
  loadSessionRecord,
  saveIdentityRecord,
  setActiveIdentity,
} from "../lib/storage.js";
import { fromBase64url } from "../lib/vault.js";
import { modeNow, resolveMode } from "../lib/mode.js";
import { beginSignInApproval } from "../actions/signin";
import { signInCodeFromAppUrl } from "../lib/app-link";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { refreshSession, useSession, type ModeInfo } from "../state/session";

interface CapacitorAppPlugin {
  addListener(
    event: "appUrlOpen",
    handler: (event: { url?: string }) => void,
  ): Promise<{ remove(): void }> | { remove(): void };
  getLaunchUrl(): Promise<{ url?: string } | undefined>;
}

/**
 * A hand-off from the launcher arrives as `#claim=<base64url JSON>` and is
 * adopted before anything else looks at storage (EPIC-018 E18-T3). The
 * fragment is always stripped, adopted or not: it carries a wrapped key record.
 */
export function adoptHandOff(): boolean {
  const hash = globalThis.location?.hash ?? "";
  if (!hash.startsWith("#claim=")) return false;
  try {
    const decoded = JSON.parse(new TextDecoder().decode(fromBase64url(hash.slice("#claim=".length))));
    if (!decoded?.identity || !decoded?.record) return false;
    saveIdentityRecord(decoded.identity, decoded.record);
    setActiveIdentity(decoded.identity);
    return true;
  } catch {
    return false;
  } finally {
    history.replaceState(null, "", globalThis.location.pathname + globalThis.location.search);
  }
}

/** Title and description per door (E15-T12). */
export function setDocumentIdentity(info: ModeInfo) {
  const title =
    ({
      launcher: "Poweur ID — claim your name",
      identity: info.subject ? `${info.subject} — Poweur ID` : "Poweur ID",
      shell: "Poweur ID",
    } as Record<string, string>)[info.mode] ?? "Poweur ID";
  const description =
    ({
      launcher: "Claim an identity you own: encrypted messages, a synced drive, and sign-in — under your own name.",
      identity: `Sign in to ${info.subject || "this identity"} with a passkey.`,
    } as Record<string, string>)[info.mode] ?? "Encrypted, identity-first messaging.";

  document.title = title;
  let meta = document.querySelector('meta[name="description"]');
  if (!meta) {
    meta = document.createElement("meta");
    meta.setAttribute("name", "description");
    document.head.appendChild(meta);
  }
  meta.setAttribute("content", description);
}

let booted = false;
/** A poweur:// open is on screen; the relay's mode answer must not replace it. */
let protectAuthRoute = false;
let removeAppUrlListener: (() => void) | undefined;
let listenerGen = 0;
let markRouteChosen: () => void = () => {};
let routeChosen = Promise.resolve();

function capacitorApp(): CapacitorAppPlugin | undefined {
  return (globalThis as { Capacitor?: { Plugins?: { App?: CapacitorAppPlugin } } }).Capacitor?.Plugins?.App;
}

function locationAuthInput() {
  return new URL(globalThis.location?.href ?? "http://localhost/").searchParams.get("auth") || "";
}

/** Settings → Approve sign-in, with the request filled in and checked. */
function presentSignInLink(input: string, requireCode = false) {
  const value = input.trim();
  if (!value) return;
  protectAuthRoute = true;
  const auth = useData.getState().auth;
  const route = useRoute.getState();
  // Cold start can deliver the same link twice (launch URL and appUrlOpen).
  if (route.page === "settings" && route.sub === "auth" && auth.input === value && auth.loading) return;
  useRoute.setState({ page: "settings", sub: "auth", params: {} });
  void beginSignInApproval(value, { requireCode });
}

function installAppUrlListener() {
  const app = capacitorApp();
  if (!app?.addListener || removeAppUrlListener) return;
  const gen = ++listenerGen;
  const result = app.addListener("appUrlOpen", (event) => {
    const input = signInCodeFromAppUrl(event?.url ?? "");
    if (input) presentSignInLink(input, true);
  });
  void Promise.resolve(result).then((handle) => {
    if (gen !== listenerGen) {
      handle?.remove();
      return;
    }
    removeAppUrlListener = () => handle?.remove();
  });
}

function readLaunchUrl(): Promise<string> {
  const app = capacitorApp();
  if (!app?.getLaunchUrl) return Promise.resolve("");
  return Promise.resolve()
    .then(() => app.getLaunchUrl())
    .then((launch) => signInCodeFromAppUrl(launch?.url ?? "") ?? "")
    .catch(() => "");
}

/**
 * Resolves once the first screen is chosen. In the shell that waits for a
 * `poweur://` launch URL; it does not wait for the relay.
 */
export function whenFirstRouteChosen(): Promise<void> {
  return routeChosen;
}

/** Runs once, before the first render. Returns the mode lookup for tests. */
export function boot(): Promise<void> {
  if (booted) return Promise.resolve();
  booted = true;
  installAppUrlListener();
  routeChosen = new Promise((resolve) => {
    markRouteChosen = resolve;
  });

  const run = (fromLaunch: string) => {
    const pending = startBoot(fromLaunch);
    markRouteChosen();
    return pending;
  };

  // The webview never navigates to poweur://; the shell hands that URL over.
  // Wait for it before choosing the first screen so the approval page is the
  // first paint. A browser has no launch URL and decides synchronously.
  if (isShellRuntime() && capacitorApp()?.getLaunchUrl) {
    return readLaunchUrl().then(run);
  }
  return run("");
}

function startBoot(fromLaunch: string): Promise<void> {
  const authInput = fromLaunch || locationAuthInput();
  const handedOver = adoptHandOff();
  refreshSession();
  const identity = getActiveIdentity();
  const route = useRoute.getState();

  if (authInput) {
    // A QR open is always another device's screen. A `?auth=` page load is
    // the same browser that started the sign-in.
    presentSignInLink(authInput, Boolean(fromLaunch));
  } else if (!protectAuthRoute && handedOver) {
    // Locked on arrival: the passkey that opens the keys is scoped to the
    // domain both hosts share (E18-T4).
    useRoute.setState({ page: "messages" });
    route.push("unlock");
  } else if (!protectAuthRoute && shouldOpenStoredIdentity(modeNow())) {
    openStoredIdentity(identity);
  } else if (!protectAuthRoute) {
    // Unknown until the relay answers, or a launcher. A stored identity must
    // not paint here: the launcher is a public claim page, and the record
    // left behind by a claim belongs on the identity's own host.
    useRoute.setState({ page: "messages", sub: null, params: {} });
  }

  // The first paint came from the cached mode (or `unknown`, the generic
  // welcome); correct the door when the relay answers.
  return resolveMode().then((info: ModeInfo) => {
    setDocumentIdentity(info);
    useSession.setState({ mode: info });
    if (authInput || protectAuthRoute || handedOver) return;
    if (info.mode === "launcher") {
      settleLauncherDoor();
      return;
    }
    // The host was unknown on the first paint. Open a stored identity only
    // if the visitor is still on that paint — a click in between stands.
    const current = useRoute.getState();
    if (current.page === "messages" && current.sub === null) openStoredIdentity(getActiveIdentity());
  });
}

/**
 * A launcher host is the claim page for everyone. An identity cached on that
 * origin (the claim hand-off writes it before navigating away) is not a
 * reason to skip the landing.
 */
function shouldOpenStoredIdentity(info: ModeInfo) {
  return info.mode === "identity" || info.mode === "shell";
}

/** Inbox, or the unlock gate when the keys are not already open. */
function openStoredIdentity(identity: string | null) {
  if (!identity) {
    useRoute.setState({ page: "messages", sub: null, params: {} });
    return;
  }
  if (!getUnlockedKeys() && !isSessionValid(loadSessionRecord(identity))) {
    useRoute.getState().push("unlock");
    return;
  }
  useRoute.setState({ page: "messages", sub: null, params: {} });
}

/** Drop an automatic unlock prompt once this host is known to be the launcher. */
function settleLauncherDoor() {
  const current = useRoute.getState();
  if (current.page === "messages" && (current.sub === null || current.sub === "unlock")) {
    useRoute.setState({ page: "messages", sub: null, params: {} });
  }
}

/** Test seam. */
export function resetBootForTests() {
  booted = false;
  protectAuthRoute = false;
  listenerGen += 1;
  removeAppUrlListener?.();
  removeAppUrlListener = undefined;
  markRouteChosen();
  markRouteChosen = () => {};
  routeChosen = Promise.resolve();
}
