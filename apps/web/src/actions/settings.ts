/** What Settings changes that is not a document on the relay (from app.js). */
import { forgetAvatar } from "../state/avatars";
import { clearSnapshots } from "../lib/snapshot";
import { clampPowBits } from "@poweur/client";
import { INBOX_MODES, type InboxPolicy } from "../lib/policy";
import { forgetNativeSecret } from "../lib/native.js";
import {
  listIdentities,
  loadSessionRecord,
  removeIdentity,
  removeSessionRecord,
} from "../lib/storage.js";
import { useRoute } from "../state/route";
import { switchIdentity, touchSession, useSession } from "../state/session";
import { forgetIdHint } from "../lib/visit";
import { setLoading, toast } from "../state/ui";
import { trackAction } from "../lib/observability";
import { activeClient, errorMessage } from "./relay";

/** One-line summaries for the Inbox rows. */
export function policySummary(policy: { doc: InboxPolicy | null; loading: boolean }) {
  const doc = policy.doc;
  if (!doc) return { mode: policy.loading ? "…" : "—", anon: "—", anonOn: false, signin: "—" };
  const mode = INBOX_MODES.find((option) => option.id === doc.mode)?.label ?? String(doc.mode ?? "");
  const anon = doc.anonymous?.allow
    ? doc.anonymous.challenge === "pow"
      ? `On · ${clampPowBits(doc.anonymous.pow_bits ?? 0)} bits`
      : "On"
    : "Off";
  const services = doc.trusted_auth_services ?? [];
  const signin = services.length === 0 ? "None" : services.length === 1 ? services[0] : `${services.length} services`;
  return { mode, anon, anonOn: Boolean(doc.anonymous?.allow), signin };
}

/**
 * Does this identity have DNS credentials to configure? (E15-T10) Newer
 * records say so; older ones fall back to "is its domain one the relay hosts?".
 */
export function usesDnsPath(record: any, identity: string | null, hostedDomains: string[] = []): boolean {
  if (typeof record?.hosted === "boolean") return !record.hosted;
  const domain = String(identity ?? "").split(".").slice(1).join(".");
  return Boolean(domain) && hostedDomains.length > 0 && !hostedDomains.includes(domain);
}

export async function refreshRelaySession() {
  const client = activeClient();
  if (!client || !useSession.getState().unlocked) {
    toast("Unlock first", "warning");
    return;
  }
  setLoading(true, "Refreshing session…");
  try {
    await client.sessions.refresh(client.signer);
    setLoading(false);
    toast("Session refreshed", "success");
    touchSession();
  } catch (error) {
    setLoading(false);
    toast(errorMessage(error), "error");
  }
}

export async function revokeRelaySession() {
  const client = activeClient();
  const identity = useSession.getState().identity;
  if (!client || !useSession.getState().unlocked) {
    toast("Unlock first", "warning");
    return;
  }
  if (!identity || !loadSessionRecord(identity)) return;
  setLoading(true, "Revoking session…");
  try {
    await client.sessions.revoke(client.signer);
    setLoading(false);
    toast("Session revoked", "success");
    touchSession();
  } catch (error) {
    setLoading(false);
    toast(errorMessage(error), "error");
  }
}

/**
 * Forget an identity on this device only — it stays registered on the relay
 * and in DNS. Its hardware secret goes too: a keystore entry nothing can open,
 * kept for the life of the install, is worse than none.
 */
export function removeIdentityFromDevice(identity: string) {
  removeIdentity(identity);
  removeSessionRecord(identity);
  forgetIdHint(identity, useSession.getState().mode.domain);
  forgetNativeSecret(identity);
  forgetAvatar(identity);
  void clearSnapshots(identity);
  switchIdentity(listIdentities()[0] || null);
  useRoute.getState().go("messages");
  toast("Identity removed from device", "info");
  trackAction("remove-identity");
}
