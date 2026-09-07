/**
 * A stand-in for EPIC-011's keystore, so E15 can build the "Keys & devices"
 * surface before E11-T1 lands.
 *
 * **This is a mock, and it is meant to be deleted.** E11-T1 is in progress:
 * seed derivation and the CLI/SDK recovery path have shipped, but the three
 * relay endpoints (`PUT /identities/{id}/keystore`, `POST …/keystore/fetch`,
 * `DELETE …/keystore/{enrollment_id}`), multi-passkey enrollment and the
 * signed `policy.json` have not. The UI those produce is E15's to design, so
 * this module supplies exactly the shape E11-T1 specifies and nothing more.
 *
 * What is real here and what is not:
 *
 *  - **Real:** the enrollment that unlocked *this* browser. Its kind, wrap
 *    method and creation time come from the identity record, so the row a user
 *    sees for their own device is accurate today.
 *  - **Mocked:** every other enrollment, the roles, and all write operations.
 *    They are served from `localStorage` and flagged `mock: true`, and each
 *    write throws `KeystoreUnavailable` rather than pretending to succeed —
 *    a mock that silently "removes" a device is worse than no mock.
 *
 * When E11-T1 lands, replace the body of `listEnrollments`/`removeEnrollment`
 * with the relay calls; the shapes below are already the ones it defines.
 */

import { loadIdentityRecord } from "./storage.js";

const MOCK_KEY = "poweur:mock:keystore:";

/** Thrown by every write path until the relay endpoints exist. */
export class KeystoreUnavailable extends Error {
  constructor(operation) {
    super(`${operation} needs the relay keystore from EPIC-011 (E11-T1), which has not shipped yet`);
    this.name = "KeystoreUnavailable";
    this.epic = "EPIC-011 E11-T1";
  }
}

/** True once the relay serves `/identities/{id}/keystore`. Hard-coded for now. */
export function keystoreAvailable() {
  return false;
}

/**
 * The enrollment shape E11-T1 defines, minus the ciphertext (which is the
 * relay's to hold and is never shown).
 *
 * @typedef {object} Enrollment
 * @property {string} enrollment_id
 * @property {"passkey"|"hardware-key"|"cli-passphrase"|"recovery-kit"|"native"} kind
 * @property {"prf"|"pin"|"passphrase"|"native"} wrap
 * @property {"device"|"recovery-master"} role
 * @property {string} label
 * @property {string} created_at
 * @property {string|null} last_used_at
 * @property {boolean} current   this browser's own enrollment
 * @property {boolean} mock      not backed by a relay
 */

/** This browser's enrollment, derived from the real identity record. */
function currentEnrollment(identity) {
  const record = loadIdentityRecord(identity);
  if (!record) return null;
  const wrap = record.encryptedKeys?.kdf === "prf" ? "prf" : "pin";
  return {
    enrollment_id: record.credentialId ? `local:${record.credentialId.slice(0, 16)}` : "local:this-browser",
    kind: "passkey",
    wrap,
    role: "device",
    label: "This browser",
    created_at: record.createdAt ?? null,
    last_used_at: new Date().toISOString(),
    current: true,
    mock: false,
  };
}

function mockExtras(identity) {
  try {
    const raw = localStorage.getItem(MOCK_KEY + identity);
    return raw ? JSON.parse(raw) : [];
  } catch {
    return [];
  }
}

/**
 * Every enrollment for an identity.
 *
 * Until E11-T1 ships this is one real row plus whatever `seedMockEnrollments`
 * put there, and the caller is expected to surface the `mock` flag rather than
 * present them as fact.
 */
export async function listEnrollments(identity) {
  const current = currentEnrollment(identity);
  return [...(current ? [current] : []), ...mockExtras(identity)];
}

/** Populate the mock rows — used by the UI preview and by tests. */
export function seedMockEnrollments(identity, enrollments) {
  localStorage.setItem(
    MOCK_KEY + identity,
    JSON.stringify(enrollments.map((e) => ({ ...e, current: false, mock: true }))),
  );
}

export function clearMockEnrollments(identity) {
  localStorage.removeItem(MOCK_KEY + identity);
}

/** E11-T1: enroll another authenticator by re-wrapping the seed under its PRF. */
export async function enrollAuthenticator() {
  throw new KeystoreUnavailable("Enrolling another device");
}

/** E11-T2: only a `recovery-master` may remove another enrollment. */
export async function removeEnrollment() {
  throw new KeystoreUnavailable("Removing an enrollment");
}

/** E11-T1: BIP39 encoding of the master seed, for hand-copying. */
export async function generateRecoveryKit() {
  throw new KeystoreUnavailable("Generating a recovery kit");
}

/**
 * Whether this identity could produce a recovery kit at all.
 *
 * Identities registered before the seed model have two independent keys and no
 * seed to encode; E11-T1 offers them rotate-to-seed rather than forcing it.
 * The web app has always generated independent keys, so today this is always
 * "legacy" — stated here so the UI can say so honestly instead of offering a
 * kit that cannot exist.
 */
export function recoveryKitEligibility(identity) {
  const record = loadIdentityRecord(identity);
  if (!record) return { eligible: false, reason: "unknown-identity" };
  return record.seedDerived
    ? { eligible: true, reason: null }
    : { eligible: false, reason: "legacy-keypair" };
}
