/**
 * Keys, devices and recovery — the web half of EPIC-011.
 *
 * The relay holds one **wrapped copy of the master seed per enrolled
 * authenticator**, as ciphertext it cannot open. Three operations, and the
 * asymmetry between them is the whole design:
 *
 *  - **list** is signed by the identity key. You are unlocked when you manage
 *    devices, so it needs no authenticator prompt, and it returns metadata
 *    only — never ciphertext, never credential ids.
 *  - **enroll** is signed by the identity key too, for the same reason.
 *  - **fetch** is authenticated by a *WebAuthn assertion instead*, because it
 *    runs precisely when the identity key is gone. Reading the seed from
 *    `poweur-sys/` would need a DAV token signed by that key — the circularity
 *    this endpoint exists to break.
 *
 * Everything here is real: this replaces the mock E15-T1 shipped while E11-T1
 * was in flight.
 */

import {
  KeystoreApi, RelayClient,
  newRecoveryKit, mnemonicToSeed, validMnemonic, seedToMnemonic,
  toBase64url, fromBase64,
} from "@poweur/client";

import { assertChallenge, credentialRpId, wrapKeysWithPRF } from "./passkey.js";
import { loadIdentityRecord, relayUrlFor, rpIdFor, saveIdentityRecord, isShellRuntime } from "./storage.js";
import { describeThisDevice } from "./devices.js";
import { jwksFromSeed, publicKeyFromJwk } from "./vault.js";

/** A stable id for an enrollment, independent of the credential it wraps. */
function newEnrollmentId() {
  return toBase64url(crypto.getRandomValues(new Uint8Array(16)));
}

function apiFor(relayUrl) {
  return new KeystoreApi(new RelayClient(relayUrl));
}

// ─── Inventory ────────────────────────────────────────────────────────────────

/**
 * Every enrollment the relay holds, annotated with which one is this browser.
 *
 * The relay withholds credential ids, so "is this me?" is answered from the
 * local record's `enrollmentId` rather than by matching credentials.
 */
export async function listEnrollments(client, identity) {
  const { enrollments } = await client.keystore.list(client.signer, identity);
  const mine = loadIdentityRecord(identity)?.enrollmentId ?? null;
  return enrollments.map((entry) => ({ ...entry, current: entry.enrollment_id === mine }));
}

// ─── Enrolling this browser ───────────────────────────────────────────────────

/**
 * Put this browser's wrapped copy in the relay keystore.
 *
 * Without this the identity lives in exactly one `localStorage` and clearing
 * site data destroys it — the failure EPIC-011 exists to remove. Called at
 * registration and offered afterwards for identities that predate it.
 *
 * Native-keystore enrollments are stored too; they cannot authorize a
 * WebAuthn bootstrap fetch, which `canBootstrap` reports.
 */
export async function enrollThisBrowser(client, identity, { label = deviceLabel() } = {}) {
  const record = loadIdentityRecord(identity);
  if (!record) throw new Error(`No local record for ${identity}`);
  const kdf = record.encryptedKeys?.kdf;
  if (kdf !== "prf" && kdf !== "native") {
    throw new Error(
      "This browser's keys are not wrapped with passkey PRF, so they cannot be backed up. " +
      "Use Safari or Chrome with Apple or Google passkeys, or the Poweur mobile app.",
    );
  }
  if (kdf === "prf" && !record.credentialPublicKey) {
    throw new Error(
      "This browser's passkey did not expose a public key, so the relay cannot verify it. " +
      "Add a device from one that can, or keep a recovery kit.",
    );
  }

  const enrollmentId = record.enrollmentId ?? newEnrollmentId();
  const { kdf: _kdf, ...wrapped } = record.encryptedKeys;

  await client.keystore.enroll(client.signer, identity, {
    enrollmentId,
    kind: kdf === "native" ? "native" : "passkey",
    wrap: kdf === "native" ? "native" : "prf",
    payload: record.seedDerived ? "seed" : "legacy-keypair",
    wrapped,
    ...(kdf === "prf" ? {
      credentialId: record.credentialId,
      credentialPublicKey: record.credentialPublicKey,
      ...(record.credentialAlg ? { credentialAlg: record.credentialAlg } : {}),
    } : {}),
    label,
  });

  saveIdentityRecord(identity, { ...record, enrollmentId, enrolledAt: new Date().toISOString() });
  return { enrollmentId, canBootstrap: kdf === "prf" };
}

/**
 * A name a person will recognise in a device list, from the user agent.
 *
 * The shell's WebView still reports Safari or Chrome. That is the wrapper,
 * not the product — the key lives in the OS keystore, so the inventory
 * should say "iPhone", not "Safari on iOS".
 */
export function deviceLabel(userAgent = globalThis.navigator?.userAgent ?? "", { native = isShellRuntime() } = {}) {
  return describeThisDevice(userAgent, { native }).name;
}

// ─── Removal ──────────────────────────────────────────────────────────────────

/**
 * Remove an enrollment, optionally ending its sessions — the "I lost my phone"
 * path.
 *
 * Once a `recovery-master` exists the relay demands an assertion from it, so
 * that a stolen phone cannot evict the very security key meant to revoke it.
 * We attempt without one and add it only when asked, rather than prompting an
 * authenticator that may not be needed.
 */
export async function removeEnrollment(client, identity, enrollmentId, { revokeSessions = true } = {}) {
  // The relay verifies the assertion against the scope the credential was
  // created with (EPIC-018 E18-T4), so both sides must name the same one.
  const scope = rpIdFor(identity);
  const options = { revokeSessions, rpId: scope };
  try {
    await client.keystore.remove(client.signer, identity, enrollmentId, options);
  } catch (error) {
    if (!needsRecoveryMaster(error)) throw error;
    const api = apiFor(relayUrlFor(identity));
    const { assertion } = await assertChallenge(await api.challenge(identity), { rpId: scope });
    await client.keystore.remove(client.signer, identity, enrollmentId, {
      ...options,
      actorAssertion: assertion,
    });
  }
}

function needsRecoveryMaster(error) {
  const message = String(error?.message ?? "").toLowerCase();
  return message.includes("recovery-master") || message.includes("actor_assertion") ||
    message.includes("actor assertion");
}

// ─── Bootstrap recovery ───────────────────────────────────────────────────────

/**
 * Recover an identity on a browser that holds nothing for it.
 *
 * This is the "I cleared site data" path, and the only one that needs no
 * identity key: an assertion from any enrolled authenticator authorizes the
 * read, and that authenticator's PRF output opens the blob it wrapped.
 *
 * `credentialId` is deliberately not passed — this browser has no record of
 * which credential to ask for, so the platform offers its discoverable ones.
 * `residentKey: "required"` at creation is what makes that possible.
 *
 * The caller writes the local record with `restoreLocalRecord` — same
 * credential, same enrollment, no second passkey.
 */
export async function recoverFromKeystore(identity, { relayUrl = relayUrlFor(identity) } = {}) {
  const api = apiFor(relayUrl);
  // Nothing is stored for this identity here, so the scope cannot be read back
  // from a record: derive the one it *would* have been created with.
  const scope = credentialRpId(identity);
  const { assertion, prfOutput } = await assertChallenge(await api.challenge(identity), { rpId: scope });
  if (!prfOutput) {
    throw new Error(
      "This authenticator did not return a PRF secret, so it cannot open the stored copy. " +
      "Use your recovery kit instead.",
    );
  }

  const { entries } = await api.fetch(identity, assertion, scope);
  const entry = entries.find((e) => e.credential_id === assertion.credential_id) ?? entries[0];
  if (!entry) throw new Error("No stored copy for this identity");

  const { unwrapKeysAES } = await import("./vault.js");
  const opened = await unwrapKeysAES(prfOutput, entry.wrapped);
  return {
    entry,
    assertion,
    signingJWK: opened.signingJWK,
    encJWK: opened.encJWK,
    seed: opened.seed ?? null,
  };
}

/**
 * Rebuild the local identity record from the authenticator that just opened
 * a keystore copy.
 *
 * This is the cleared-site-data path: the passkey already exists, its PRF
 * already unwrapped the blob, and minting a second credential would ask the
 * user to "add a passkey" they just used. The enrollment row stays put —
 * reusing it does not overwrite another authenticator's copy, because this
 * *is* that authenticator.
 *
 * New-device join still mints a fresh passkey (`adoptIdentity`); this helper
 * is only for the same authenticator coming back.
 */
export function restoreLocalRecord(identity, recovered, { relayUrl }) {
  const { entry, assertion, signingJWK, encJWK, seed } = recovered ?? {};
  const credentialId = assertion?.credential_id || entry?.credential_id;
  if (!entry?.enrollment_id || !credentialId || !entry.wrapped) {
    throw new Error("Nothing to restore — the authenticator did not match a stored copy.");
  }
  const wrapped = typeof entry.wrapped === "object" ? { ...entry.wrapped } : {};
  const record = {
    identity,
    publicKey: publicKeyFromJwk(signingJWK),
    encPublicKey: publicKeyFromJwk(encJWK),
    credentialId,
    credentialPublicKey: entry.credential_public_key ?? null,
    credentialAlg: entry.credential_alg ?? null,
    rpId: credentialRpId(identity),
    encryptedKeys: { ...wrapped, kdf: "prf" },
    relay: relayUrl,
    userId: toBase64url(crypto.getRandomValues(new Uint8Array(16))),
    createdAt: entry.created_at || new Date().toISOString(),
    supportsPRF: true,
    seedDerived: Boolean(seed) || entry.payload === "seed",
    enrollmentId: entry.enrollment_id,
  };
  saveIdentityRecord(identity, record);
  return record;
}

// ─── Recovery kit ─────────────────────────────────────────────────────────────

/**
 * Whether a kit can exist for this identity.
 *
 * A kit is the master seed as 24 words. An identity generated as two
 * independent keys has no seed to encode — the normal case for anything the
 * web app registered before EPIC-011 — and rotating to fix that makes contacts
 * re-pin the key, so it is offered, never forced.
 */
export function recoveryKitEligibility(identity) {
  const record = loadIdentityRecord(identity);
  if (!record) return { eligible: false, reason: "unknown-identity" };
  return record.seedDerived
    ? { eligible: true, reason: null }
    : { eligible: false, reason: "legacy-keypair" };
}

/**
 * The kit itself: 24 words plus the base64url seed.
 *
 * Needs the unlocked seed, which only lives in the wrapped blob — so this is
 * available exactly when the identity is open, and never from the relay.
 */
export function buildRecoveryKit(identity, seedBase64url) {
  if (!seedBase64url) throw new Error("This identity has no master seed to encode");
  return newRecoveryKit(identity, relayUrlFor(identity), fromBase64(seedBase64url));
}

/** Does the user's re-typed kit decode to the seed we handed them? */
export function verifyRecoveryKit(mnemonic, seedBase64url) {
  if (!validMnemonic(mnemonic)) return false;
  try {
    return toBase64url(mnemonicToSeed(mnemonic)) === seedBase64url;
  } catch {
    return false;
  }
}

export { seedToMnemonic, validMnemonic, mnemonicToSeed };

/** Rebuild an identity's keys from a kit, for restore-from-paper. */
export function keysFromMnemonic(mnemonic) {
  const seed = mnemonicToSeed(mnemonic);
  return { ...jwksFromSeed(seed), seed: toBase64url(seed) };
}

/** Re-wrap an opened identity under this browser's PRF secret. */
export async function rewrap({ prfOutput = null }, { signingJWK, encJWK, seed }) {
  if (!prfOutput) throw new Error("A PRF secret is required to store keys in this browser");
  return wrapKeysWithPRF(prfOutput, signingJWK, encJWK, seed);
}
