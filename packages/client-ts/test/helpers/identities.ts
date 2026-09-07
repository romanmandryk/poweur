/** Create hosted identities against a running relay, ready to send. */

import { PoweurClient } from "../../src/client.js";
import { signerFor, type StoredIdentityKeys } from "../../src/crypto/keys.js";
import { createIdentity, IdentityApi } from "../../src/identity.js";
import { RelayClient } from "../../src/http.js";
import { MemorySessionStore } from "../../src/session.js";
import type { ResolveOptions } from "../../src/resolve.js";

export interface TestIdentity {
  identity: string;
  keys: StoredIdentityKeys;
  client: PoweurClient;
}

/**
 * Resolution against a local relay: plain HTTP, loopback allowed, and DNS
 * skipped (there is no zone). The relay's `GET /identities/{id}` serves the
 * signed document, which is what `relayUrl` reaches.
 */
export function localResolveOptions(relayUrl: string): ResolveOptions {
  return { scheme: "http", allowPrivate: true, skipDns: true, relayUrl };
}

let counter = 0;

export function uniqueIdentity(prefix: string): string {
  counter += 1;
  return `${prefix}${Date.now().toString(36)}${counter}.poweur.net`;
}

export async function createTestIdentity(
  relayUrl: string,
  prefix: string,
): Promise<TestIdentity> {
  const identity = uniqueIdentity(prefix);
  const api = new IdentityApi(new RelayClient(relayUrl));
  const created = await createIdentity(api, identity, { hosted: true });
  const { signer, decryptor } = signerFor(created.keys);
  return {
    identity,
    keys: created.keys,
    client: new PoweurClient({
      relayUrl,
      signer,
      decryptor,
      sessionStore: new MemorySessionStore(),
      resolve: localResolveOptions(relayUrl),
    }),
  };
}
