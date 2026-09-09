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

import { generateIdentityJwks, keyBytesFromJwks, wrapKeysAES } from "../../js/vault.js";
import { identityApiFor } from "../../js/client.js";
import { saveIdentityRecord, setActiveIdentity, setUnlockedKeys } from "../../js/storage.js";

export async function createWebIdentity(relayUrl, identity) {
  const { signingJWK, encJWK, publicKey, encPublicKey } = await generateIdentityJwks();
  const wrapSecret = crypto.getRandomValues(new Uint8Array(32));
  const encryptedKeys = { ...(await wrapKeysAES(wrapSecret, signingJWK, encJWK)), kdf: "prf" };

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
