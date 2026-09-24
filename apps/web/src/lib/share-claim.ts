import { fromBase64url } from "./vault.js";

export interface PendingShareClaim {
  share_id: string;
  owner: string;
  token: string;
  action: "viewed" | "downloaded" | "uploaded";
}

const KEY = "poweur.pending-share-claim";
let memory: PendingShareClaim | null = null;

function valid(value: any): value is PendingShareClaim {
  return Boolean(
    value && typeof value.share_id === "string" && value.share_id.length > 0 &&
    typeof value.owner === "string" && value.owner.includes(".") &&
    typeof value.token === "string" && /^[a-z2-7]{26}$/.test(value.token) &&
    ["viewed", "downloaded", "uploaded"].includes(value.action),
  );
}

export function storePendingShareClaim(value: unknown): boolean {
  if (!valid(value)) return false;
  memory = value;
  try { sessionStorage.setItem(KEY, JSON.stringify(value)); } catch { /* memory is enough */ }
  return true;
}

export function pendingShareClaim(): PendingShareClaim | null {
  if (memory) return memory;
  try {
    const raw = sessionStorage.getItem(KEY);
    const parsed = raw ? JSON.parse(raw) : null;
    if (valid(parsed)) memory = parsed;
  } catch { /* absent or unavailable */ }
  return memory;
}

export function clearPendingShareClaim() {
  memory = null;
  try { sessionStorage.removeItem(KEY); } catch { /* unavailable */ }
}

/** Capture `#share=<base64url JSON>` and remove the capability from the address bar. */
export function takeShareClaimLink(hash = globalThis.location?.hash ?? ""): boolean {
  if (!hash.startsWith("#share=")) return false;
  let stored = false;
  try {
    const decoded = JSON.parse(new TextDecoder().decode(fromBase64url(hash.slice("#share=".length))));
    stored = storePendingShareClaim(decoded);
  } catch {
    stored = false;
  } finally {
    try { history.replaceState(null, "", globalThis.location.pathname + globalThis.location.search); } catch { /* best effort */ }
  }
  return stored;
}

export function resetPendingShareClaimForTests() {
  clearPendingShareClaim();
}
