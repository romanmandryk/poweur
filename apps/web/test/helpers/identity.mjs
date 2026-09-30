/**
 * Register an identity exactly the way `app.js:doCreateIdentity` does, minus
 * the passkey ceremony: WebCrypto JWKs, wrapped locally, registered through
 * `@poweur/client`, then stored as an identity record.
 *
 * Tests that use this are asserting the *adapter* — that a key held as a
 * WebCrypto JWK and signed through `WebCryptoSigner` is accepted by a real
 * relay — which is the part `apps/web` still owns after E15-T6.
 */
import { createIdentity, rfc3339 } from "@poweur/client";

import { generateSeedIdentityJwks, keyBytesFromJwks, wrapKeysAES } from "../../src/lib/vault.js";
import { identityApiFor } from "../../src/lib/client.js";
import { saveIdentityRecord, setActiveIdentity, setUnlockedKeys } from "../../src/lib/storage.js";

export async function createWebIdentity(relayUrl, identity) {
  const { signingJWK, encJWK, publicKey, encPublicKey, seed } = await generateSeedIdentityJwks();
  const wrapSecret = crypto.getRandomValues(new Uint8Array(32));
  const encryptedKeys = { ...(await wrapKeysAES(wrapSecret, signingJWK, encJWK, seed)), kdf: "prf" };

  await createIdentity(identityApiFor(relayUrl), identity, {
    hosted: true,
    keys: keyBytesFromJwks(identity, signingJWK, encJWK),
  });

  saveIdentityRecord(identity, {
    identity, publicKey, encPublicKey,
    credentialId: "test-credential",
    encryptedKeys,
    relay: relayUrl,
    userId: "test-user",
    createdAt: rfc3339(),
    supportsPRF: true,
  });

  return { identity, signingJWK, encJWK, publicKey, encPublicKey, wrapSecret };
}

/** Make `identity` the unlocked, active one — what `clientFor()` needs. */
export function unlock({ identity, signingJWK, encJWK }) {
  setUnlockedKeys(identity, signingJWK, encJWK);
  setActiveIdentity(identity);
}
