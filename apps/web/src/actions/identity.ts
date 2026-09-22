/**
 * Identity lifecycle, ported from app.js (E21-T6): choose custody, create an
 * identity, unlock it, restore it from the relay keystore, adopt one that
 * another device approved, and hand a fresh claim to its own origin.
 *
 * Every function reports through toasts and the loading overlay, as the legacy
 * ones did, so screens can call them straight from a click handler.
 */
import {
  createIdentity as registerIdentity,
  EnrollApi,
  isSessionValid,
  RelayClient,
  resolveRecipientRelayUrl,
} from "@poweur/client";
import { clientFor, identityApiFor, resolveOptionsForRelay } from "../lib/client.js";
import { enrollThisBrowser, recoverFromKeystore as recoverRecord, restoreLocalRecord, rewrap } from "../lib/keystore.js";
import { resolveMode } from "../lib/mode.js";
import {
  biometricAvailability,
  GATE_BIOMETRIC,
  hasNativeKeystore,
  unwrapKeysNative,
  wrapKeysNative as wrapKeysNativeJs,
} from "../lib/native.js";
import {
  authenticatePasskey,
  checkPasskeySupport,
  createPasskey,
  PRF_UNAVAILABLE_MESSAGE,
  unwrapKeysWithPRF,
  wrapKeysWithPRF as wrapKeysWithPRFJs,
} from "../lib/passkey.js";
import {
  clearUnlockedKeys,
  defaultRelayUrl,
  getConfig,
  identityOriginUrl,
  loadIdentityRecord,
  loadSessionRecord,
  removeIdentity,
  rpIdFor,
  saveConfig,
  saveIdentityRecord,
  setUnlockedKeys as setUnlockedKeysJs,
} from "../lib/storage.js";

// The carried modules default `seed = null`, which TypeScript reads as "seed is
// always null". They take a base64url seed; say so here rather than editing
// files the freeze guard keeps byte-identical to apps/web.
type Seed = string | null | undefined;
const setUnlockedKeys = setUnlockedKeysJs as (identity: string, signingJWK: unknown, encJWK: unknown, seed?: Seed) => void;
const wrapKeysNative = wrapKeysNativeJs as (
  identity: string,
  signingJWK: unknown,
  encJWK: unknown,
  seed?: Seed,
  options?: { gate?: string; reason?: string },
) => Promise<unknown>;
const wrapKeysWithPRF = wrapKeysWithPRFJs as (prfOutput: unknown, signingJWK: unknown, encJWK: unknown, seed?: Seed) => Promise<unknown>;
import { generateSeedIdentityJwks, keyBytesFromJwks, publicKeyFromJwk, toBase64url } from "../lib/vault.js";
import { identityAppUrl, type CustodyChoice } from "../lib/claim";
import { useData } from "../state/data";
import { useRoute, type SubPageId } from "../state/route";
import { afterUnlock, lockIdentity, refreshSession, switchIdentity, useSession } from "../state/session";
import { setLoading, toast } from "../state/ui";
import { trackAction } from "../lib/observability";

const message = (error: unknown) => (error as Error)?.message ?? String(error);

/**
 * Which custody this device should use for a *new* record (EPIC-019 E19-T2):
 * a biometric-gated hardware keystore beats a passkey on both ends; a passkey
 * with PRF comes next; authenticators without PRF are refused.
 */
export async function chooseCustody(): Promise<CustodyChoice> {
  if (!hasNativeKeystore()) return { kind: "passkey", reason: "no_native_keystore" };
  const biometrics = await biometricAvailability();
  return biometrics.available
    ? { kind: "native", gate: GATE_BIOMETRIC, biometryKind: biometrics.kind ?? null }
    : { kind: "passkey", reason: biometrics.reason ?? "unavailable" };
}

/** Throws with the reason when this browser cannot hold keys behind a PRF passkey. */
async function requirePasskeyUnlessNative(custody: CustodyChoice) {
  if (custody.kind === "native") return;
  const support = await checkPasskeySupport();
  if (!support.available || support.prf === false) throw new Error(support.reason || PRF_UNAVAILABLE_MESSAGE);
}

/** Register (or reuse) a relay session for an unlocked identity. */
export async function ensureSession(identity: string) {
  const client: any = clientFor(identity);
  if (!client) return null;
  return client.sessions.ensure(client.signer);
}

/**
 * Sign in with a passkey this browser already has. Nothing stored here? The
 * relay may hold a copy this passkey can open — the "I cleared site data" path
 * (EPIC-011 E11-T1).
 */
export async function signInWithPasskey(identity: string) {
  const fqdn = String(identity ?? "").trim().toLowerCase();
  if (!fqdn) {
    toast("Enter your identity (e.g. alice.poweur.net)", "warning");
    return;
  }
  if (!loadIdentityRecord(fqdn)) {
    await recoverFromKeystore(fqdn);
    return;
  }
  switchIdentity(fqdn);
  useRoute.getState().push("unlock");
}

/** Open the active identity's keys with whatever custody holds them. */
export async function unlock() {
  const identity = useSession.getState().identity;
  const record: any = identity ? loadIdentityRecord(identity) : null;
  if (!identity || !record) {
    toast("Identity record not found", "error");
    return;
  }

  setLoading(true, "Authenticating…");
  try {
    let opened: any;
    if (record.encryptedKeys?.kdf === "native") {
      // The platform draws its own prompt; an overlay on top would narrate it.
      setLoading(false);
      opened = await unwrapKeysNative(identity, record.encryptedKeys, { reason: `Unlock ${identity.split(".")[0]}` });
      setLoading(true, "Unlocking…");
    } else if (record.encryptedKeys?.kdf === "prf") {
      const { prfOutput } = await authenticatePasskey(record.credentialId, { rpId: rpIdFor(identity) });
      if (!prfOutput) throw new Error(PRF_UNAVAILABLE_MESSAGE);
      opened = await unwrapKeysWithPRF(prfOutput, record.encryptedKeys);
    } else {
      throw new Error(PRF_UNAVAILABLE_MESSAGE);
    }

    setUnlockedKeys(identity, opened.signingJWK, opened.encJWK, opened.seed ?? null);
    if (!isSessionValid(loadSessionRecord(identity))) {
      setLoading(true, "Creating session…");
      await ensureSession(identity);
    }

    setLoading(false);
    toast("Unlocked", "success");
    const returnTo = useRoute.getState().params?.returnTo as SubPageId | undefined;
    useRoute.setState({ sub: returnTo || null, params: {} });
    afterUnlock();
  } catch (error) {
    setLoading(false);
    toast(message(error), "error");
  }
}

/**
 * Restore an identity onto a browser that holds nothing for it, authorized by
 * a WebAuthn assertion alone.
 */
export async function recoverFromKeystore(identity: string) {
  const relayUrl = defaultRelayUrl();
  setLoading(true, "Looking for a stored copy…");
  let recovered: any;
  try {
    recovered = await recoverRecord(identity, { relayUrl });
  } catch (error) {
    setLoading(false);
    toast(`Could not restore ${identity}: ${message(error)}`, "error", 9000);
    return;
  }

  try {
    restoreLocalRecord(identity, recovered, { relayUrl });
    switchIdentity(identity);
    setUnlockedKeys(identity, recovered.signingJWK, recovered.encJWK, recovered.seed);
    refreshSession();

    setLoading(true, "Creating session…");
    await ensureSession(identity);

    setLoading(false);
    toast(`${identity} restored on this device`, "success", 5000);
    useRoute.setState({ page: "messages", sub: null, params: {} });
    afterUnlock();
  } catch (error) {
    setLoading(false);
    toast(message(error), "error", 9000);
  }
}

/**
 * Take ownership of key material this browser did not generate — a device
 * joining via the enrollment ceremony. Wraps it locally and registers a new
 * enrollment.
 */
export async function adoptIdentity({
  identity,
  relayUrl,
  signingJWK,
  encJWK,
  seed,
  label,
}: {
  identity: string;
  relayUrl: string;
  signingJWK: any;
  encJWK: any;
  seed: string;
  label?: string;
}) {
  const custody = await chooseCustody();
  await requirePasskeyUnlessNative(custody);

  const userId = toBase64url(crypto.getRandomValues(new Uint8Array(16)));
  let credentialId = "";
  let supportsPRF = false;
  let credentialPublicKey = null;
  let credentialAlg = null;
  let credentialScope = null;
  let encryptedKeys;
  if (custody.kind === "native") {
    setLoading(false);
    encryptedKeys = await wrapKeysNative(identity, signingJWK, encJWK, seed, {
      gate: custody.gate,
      reason: `Protect ${identity.split(".")[0]}`,
    });
    setLoading(true, "Securing keys…");
  } else {
    setLoading(true, "Creating a passkey on this device…");
    const created: any = await createPasskey(identity, userId);
    ({ credentialId, supportsPRF, credentialPublicKey, credentialAlg } = created);
    credentialScope = created.rpId;
    setLoading(true, "Securing keys…");
    encryptedKeys = await rewrap({ prfOutput: created.prfOutput }, { signingJWK, encJWK, seed });
  }

  saveIdentityRecord(identity, {
    identity,
    publicKey: publicKeyFromJwk(signingJWK),
    encPublicKey: publicKeyFromJwk(encJWK),
    credentialId,
    credentialPublicKey,
    credentialAlg,
    rpId: credentialScope,
    encryptedKeys,
    relay: relayUrl,
    userId,
    createdAt: new Date().toISOString(),
    supportsPRF,
    seedDerived: Boolean(seed),
    // A fresh passkey is a fresh enrollment.
    enrollmentId: null,
  });

  switchIdentity(identity);
  setUnlockedKeys(identity, signingJWK, encJWK, seed);
  refreshSession();

  setLoading(true, "Creating session…");
  await ensureSession(identity);

  setLoading(true, "Registering this device…");
  await enrollThisBrowser(clientFor(identity), identity, { label }).catch((error: unknown) => {
    console.warn("Keystore enrollment failed:", message(error));
    toast("Restored, but this device is not backed up — see Settings → Keys & devices", "warning", 8000);
  });

  setLoading(false);
  useRoute.setState({ page: "messages", sub: null, params: {} });
  afterUnlock();
}

/** API base for an identity this device does not yet hold. */
export async function enrollApiForJoin(identity: string): Promise<{ enroll: any; relayUrl: string }> {
  const fallback: string = defaultRelayUrl();
  const origin: string = identityOriginUrl(identity, fallback);
  let fallbackHost = "";
  try {
    fallbackHost = new URL(fallback).hostname.toLowerCase();
  } catch {
    /* ignore */
  }
  // Already on this identity's host — stay there.
  if (origin && fallbackHost === String(identity).toLowerCase()) {
    return { enroll: new EnrollApi(new RelayClient(origin)), relayUrl: origin };
  }
  // Hosted identity hosts *are* the relay; probe so a website serving only
  // well-known is not treated as one.
  if (origin) {
    try {
      const root: any = await (identityApiFor(origin, { timeoutMs: 4000 }) as any).root();
      if (root?.service === "poweur-relay") {
        return { enroll: new EnrollApi(new RelayClient(origin)), relayUrl: origin };
      }
    } catch {
      /* a website, not a relay */
    }
  }
  try {
    const relayUrl = await resolveRecipientRelayUrl(
      identity,
      fallback.startsWith("http://") ? "http" : "https",
      resolveOptionsForRelay(fallback),
    );
    return { enroll: new EnrollApi(new RelayClient(relayUrl)), relayUrl };
  } catch {
    return { enroll: new EnrollApi(new RelayClient(fallback)), relayUrl: fallback };
  }
}

export interface ClaimIntent {
  handle: string;
  domain: string;
  hosted: boolean;
  provider?: string;
  dnsToken?: string;
  inviteCode?: string;
}

/**
 * Move a freshly claimed identity from the launcher host to its own origin.
 * The record travels in the fragment — never sent to a server — and the
 * passkey is already scoped to the registrable domain both hosts share.
 */
export async function handOffToIdentityOrigin(identity: string): Promise<boolean> {
  const record = loadIdentityRecord(identity);
  const host = globalThis.location?.hostname ?? "";
  if (!record || !host || host === identity) return false;

  const info = await resolveMode();
  if (info.mode !== "launcher") return false;

  const payload = toBase64url(new TextEncoder().encode(JSON.stringify({ identity, record })));
  // The record travels in the fragment. Leaving it on this origin made the
  // next visit to the public launcher offer to open that identity.
  removeIdentity(identity);
  clearUnlockedKeys();
  refreshSession();
  setLoading(true, `Taking you to ${identity}…`);
  globalThis.location.href = `${identityAppUrl(identity)}#claim=${payload}`;
  return true;
}

/**
 * Create an identity from an explicit intent (E15-T7). `onNameRefused` puts
 * the user back on the name field when the relay refuses after the passkey
 * ceremony (availability is advisory and fails open).
 */
export async function createIdentity(intent: ClaimIntent, { onNameRefused }: { onNameRefused?: (handle: string) => void } = {}) {
  if (!intent?.handle || !intent?.domain) {
    toast("Choose a name first", "warning");
    return;
  }

  const { handle, domain, hosted } = intent;
  // A brand-new identity has no record yet, so this is the one moment the
  // relay is not read from one.
  const relayUrl: string = defaultRelayUrl();
  const provider = intent.provider;
  const dnsToken = intent.dnsToken ?? "";

  if (!relayUrl) {
    toast("Choose a relay first", "warning");
    return;
  }
  if (!hosted && !dnsToken) {
    toast(`This relay does not host ${domain} — enter a DNS API token for it`, "warning", 7000);
    return;
  }

  const identity = `${handle}.${domain}`;
  if (loadIdentityRecord(identity)) {
    toast("Identity already exists on this device", "warning");
    return;
  }

  const custody = await chooseCustody();
  try {
    await requirePasskeyUnlessNative(custody);
  } catch (error) {
    toast(message(error), "error", 9000);
    return;
  }

  setLoading(true, "Generating keys…");
  try {
    // Leave whoever is signed in: their profile, inbox and keys must not
    // follow this new identity into onboarding.
    lockIdentity();

    // Seed-derived from the start (EPIC-011): one secret behind both keys.
    const { signingJWK: sigPriv, encJWK: encPriv, seed, publicKey, encPublicKey } = await generateSeedIdentityJwks();

    const userId = toBase64url(crypto.getRandomValues(new Uint8Array(16)));
    let credentialId = "";
    let supportsPRF = false;
    let credentialPublicKey = null;
    let credentialAlg = null;
    let credentialScope = null;
    let encryptedKeys;
    if (custody.kind === "native") {
      setLoading(false);
      encryptedKeys = await wrapKeysNative(identity, sigPriv, encPriv, seed, { gate: custody.gate, reason: `Protect ${handle}` });
      setLoading(true, "Securing keys…");
    } else {
      setLoading(true, "Creating passkey…");
      const created: any = await createPasskey(identity, userId);
      ({ credentialId, supportsPRF, credentialPublicKey, credentialAlg } = created);
      credentialScope = created.rpId;
      setLoading(true, "Securing keys…");
      encryptedKeys = await wrapKeysWithPRF(created.prfOutput, sigPriv, encPriv, seed);
    }

    setLoading(true, "Registering identity…");
    const registered: any = await registerIdentity(identityApiFor(relayUrl) as any, identity, {
      hosted,
      keys: keyBytesFromJwks(identity, sigPriv, encPriv),
      ...(hosted ? {} : { dnsProvider: provider, dnsToken }),
      ...(intent.inviteCode ? { inviteCode: intent.inviteCode } : {}),
      // A relay may gate signup behind proof-of-work (EPIC-014); say so.
      onRegistrationChallenge: ({ bits }: { bits: number }) => setLoading(true, `This relay asks for proof of work (${bits} bits)…`),
      onRegistrationProgress: (attempts: number) => setLoading(true, `Proof of work: ${attempts.toLocaleString()} attempts…`),
    } as any);

    saveIdentityRecord(identity, {
      identity,
      publicKey,
      encPublicKey,
      credentialId,
      credentialPublicKey,
      credentialAlg,
      rpId: credentialScope,
      encryptedKeys,
      relay: relayUrl,
      // Which path this identity took: Settings shows DNS credentials only to
      // someone who has any (E15-T10).
      hosted,
      userId,
      createdAt: registered.document.updated_at,
      supportsPRF,
      seedDerived: true,
    });
    saveConfig({ ...getConfig(), relayUrl, parentDomain: domain, dnsProvider: provider });

    switchIdentity(identity);
    setUnlockedKeys(identity, sigPriv, encPriv, seed);
    refreshSession();

    setLoading(true, "Creating session…");
    await ensureSession(identity);

    // A wrapped copy on the relay now, or clearing site data destroys it.
    setLoading(true, "Registering this device…");
    await enrollThisBrowser(clientFor(identity), identity).catch((error: unknown) => {
      console.warn("Keystore enrollment failed:", message(error));
      toast("Identity created, but this device is not backed up yet — see Settings → Keys & devices", "warning", 8000);
    });
    afterUnlock();
    trackAction("create-identity", { kind: hosted ? "hosted" : "dns" });

    setLoading(false);
    toast(`${identity} created`, "success");

    // Claimed on a launcher host? The identity's own origin is where it lives.
    if (await handOffToIdentityOrigin(identity)) return;

    // A configured app beats an empty inbox (E15-T5).
    useData.setState({ onboard: { step: 1 } });
    useRoute.setState({ page: "messages", sub: "onboarding", params: {} });
  } catch (error: any) {
    setLoading(false);
    console.error(error);
    if (error?.relayCode === "handle_unavailable" || /taken|reserved|not available/i.test(error?.message ?? "")) {
      useData.setState((state) => ({ door: { ...state.door, state: "claimable", message: error.message } }));
      toast(`${error.message} The passkey you just created is unused — pick another name.`, "error", 9000);
      onNameRefused?.(handle);
      return;
    }
    toast(message(error), "error", 8000);
  }
}
