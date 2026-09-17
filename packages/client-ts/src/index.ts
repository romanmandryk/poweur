/**
 * `@poweur/client` — the Poweur protocol in TypeScript.
 *
 * Runtime-agnostic: this entry point touches no DOM API and no Node builtin.
 * Runtime glue lives behind subpath imports:
 *
 *   import { PoweurClient } from "@poweur/client";
 *   import { nodeTxtResolver, FileKeyStore } from "@poweur/client/node";
 *   import { dohTxtResolver } from "@poweur/client/browser";
 *
 * Go stays canonical (`packages/identity`); this implementation conforms to
 * it via the vectors in `test/conformance.test.ts`.
 */

export const SDK_VERSION = "0.1.8";
/** UTC `YYYY-MM-DD HH:MM` stamped when this package's patch version is bumped. */
export const SDK_BUILD_TIME = "2026-09-17 14:09";

export * from "./types.js";
export * from "./errors.js";
export * from "./encoding.js";
export * from "./canonical.js";
export * from "./keystore.js";
export * from "./kit.js";
export * from "./enroll.js";
export * from "./msgtypes.js";
export * from "./names.js";
export * from "./document.js";
export * from "./fingerprint.js";
export * from "./ids.js";
export * from "./pow.js";
export * from "./http.js";
export * from "./resolve.js";
export * from "./identity.js";
export * from "./session.js";
export * from "./messages.js";
export * from "./attachments.js";
export * from "./groups.js";
export * from "./events.js";
export * from "./files.js";
export * from "./sync.js";
export * from "./shares.js";
export * from "./contacts.js";
export * from "./policy.js";
export * from "./profile.js";
export * from "./history.js";
export * from "./apppass.js";
export * from "./signin.js";
export * from "./client.js";

export * as crypto from "./crypto/index.js";
export {
  LocalDecryptor,
  LocalSigner,
  MemoryKeyStore,
  generateIdentityKeys,
  identityKeysFromSeed,
  signerFor,
  requireKeys,
  type Decryptor,
  type KeyStore,
  type SignatureEncoding,
  type Signer,
  type StoredIdentityKeys,
} from "./crypto/keys.js";

export * from "./analytics.js";
