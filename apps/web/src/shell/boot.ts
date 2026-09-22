/**
 * Boot: decide the first screen before the first paint, then correct the front
 * door when the relay says which host this is (from app.js `boot()`).
 */
import { isSessionValid } from "@poweur/client";
import {
  getActiveIdentity,
  getUnlockedKeys,
  loadSessionRecord,
  saveIdentityRecord,
  setActiveIdentity,
} from "../lib/storage.js";
import { fromBase64url } from "../lib/vault.js";
import { modeNow, resolveMode } from "../lib/mode.js";
import { beginSignInApproval } from "../actions/signin";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { refreshSession, useSession, type ModeInfo } from "../state/session";

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

/** Runs once, before the first render. Returns the mode lookup for tests. */
export function boot(): Promise<void> {
  if (booted) return Promise.resolve();
  booted = true;

  const authInput = new URL(globalThis.location?.href ?? "http://localhost/").searchParams.get("auth") || "";
  const handedOver = adoptHandOff();
  refreshSession();
  const identity = getActiveIdentity();
  const route = useRoute.getState();

  if (authInput) {
    useRoute.setState({ page: "settings", sub: "auth", params: {} });
    useData.setState((state) => ({ auth: { ...state.auth, input: authInput } }));
    void beginSignInApproval(authInput);
  } else if (handedOver) {
    // Locked on arrival: the passkey that opens the keys is scoped to the
    // domain both hosts share (E18-T4).
    useRoute.setState({ page: "messages" });
    route.push("unlock");
  } else if (shouldOpenStoredIdentity(modeNow())) {
    openStoredIdentity(identity);
  } else {
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
    if (authInput || handedOver) return;
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
}
