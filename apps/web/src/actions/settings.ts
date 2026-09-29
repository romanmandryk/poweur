/** What Settings changes that is not a document on the relay (from app.js). */
import { forgetAvatar } from "../state/avatars";
import { clearSnapshots } from "../lib/snapshot";
import { clampPowBits } from "@poweur/client";
import { INBOX_MODES, type InboxPolicy } from "../lib/policy";
import { forgetNativeSecret, wrapKeysNative as wrapKeysNativeJs } from "../lib/native.js";
import { authenticatePasskey, PRF_UNAVAILABLE_MESSAGE, wrapKeysWithPRF as wrapKeysWithPRFJs } from "../lib/passkey.js";
import {
  getUnlockedKeys,
  listIdentities,
  loadIdentityRecord,
  loadSessionRecord,
  removeIdentity,
  removeSessionRecord,
  rpIdFor,
  saveIdentityRecord,
  setUnlockedKeys as setUnlockedKeysJs,
} from "../lib/storage.js";
import { generateEncryptionJwk } from "../lib/vault.js";
import { useRoute } from "../state/route";
import { switchIdentity, touchSession, useSession } from "../state/session";
import { forgetIdHint } from "../lib/visit";
import { setLoading, toast } from "../state/ui";
import { trackAction } from "../lib/observability";
import { activeClient, errorMessage } from "./relay";

// The carried modules default `seed = null`; they take a base64url seed.
type Seed = string | null | undefined;
const setUnlockedKeys = setUnlockedKeysJs as (identity: string, signingJWK: unknown, encJWK: unknown, seed?: Seed) => void;
const wrapKeysNative = wrapKeysNativeJs as (identity: string, s: unknown, e: unknown, seed?: Seed, options?: object) => Promise<unknown>;
const wrapKeysWithPRF = wrapKeysWithPRFJs as (prf: unknown, s: unknown, e: unknown, seed?: Seed) => Promise<unknown>;

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

/** Publish a new encryption key and re-wrap the local record around it. */
export async function rotateEncryptionKey() {
  const client = activeClient();
  const identity = useSession.getState().identity;
  if (!client || !identity) {
    toast("Unlock your identity first", "warning");
    return;
  }
  const record: any = loadIdentityRecord(identity);
  const keys: any = getUnlockedKeys();

  setLoading(true, "Rotating encryption key…");
  try {
    const { encJWK: encPrivNew, encPublicKey } = await generateEncryptionJwk();
    await client.identity.publishEncryptionKey(client.signer, encPublicKey);

    let encryptedKeys;
    if (record.encryptedKeys?.kdf === "native") {
      encryptedKeys = await wrapKeysNative(identity, keys.signingJWK, encPrivNew, keys.seed, {
        gate: record.encryptedKeys.gate,
        reason: `Protect ${identity.split(".")[0]}`,
      });
    } else {
      const { prfOutput } = await authenticatePasskey(record.credentialId, { rpId: rpIdFor(identity) });
      if (!prfOutput) throw new Error(PRF_UNAVAILABLE_MESSAGE);
      encryptedKeys = await wrapKeysWithPRF(prfOutput, keys.signingJWK, encPrivNew, keys.seed);
    }

    // The encryption key no longer derives from the seed, so a kit rebuilt
    // from it would restore the old one: say so rather than hand out a stale kit.
    saveIdentityRecord(identity, { ...record, encPublicKey, encryptedKeys, seedDerived: false });
    setUnlockedKeys(identity, keys.signingJWK, encPrivNew, keys.seed);
    setLoading(false);
    toast("Encryption key rotated", "success");
    touchSession();
  } catch (error) {
    setLoading(false);
    toast(errorMessage(error), "error");
  }
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
