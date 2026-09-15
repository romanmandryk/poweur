/**
 * The app's one bridge to `@poweur/client`.
 *
 * Every relay call in the web app goes through a `PoweurClient` built here,
 * which is what makes two EPIC-015 constraints structural rather than a
 * convention people have to remember:
 *
 *  - the relay URL comes from the identity record (`storage.relayUrlFor`),
 *    never from `location.origin`, so a Capacitor shell and a multi-relay
 *    client both work (E15-T1);
 *  - key custody stays in `js/vault.js` — the package receives a `Signer`,
 *    not key material.
 */

import { PoweurClient, IdentityApi, ResolveCache, resolveIdentity } from "@poweur/client";
import { dohTxtResolver } from "@poweur/client/browser";

import {
  BrowserSessionStore, getUnlockedKeys, relayUrlFor, resolveOptionsFor,
} from "./storage.js";
import { JwkDecryptor, WebCryptoSigner } from "./vault.js";

/** Shared across every client so one lookup serves the whole session. */
const resolveCache = new ResolveCache();

/** DNS-over-HTTPS is the only DNS a browser has; the web path is tried first. */
const txt = dohTxtResolver();

function resolveOptions(relayUrl) {
  return { ...resolveOptionsFor(relayUrl), txt, cache: resolveCache };
}

/** The unlocked identity's `Signer`/`Decryptor`, or null while locked. */
export function signerFor(identity) {
  const keys = getUnlockedKeys();
  if (!keys || keys.identity !== identity) return null;
  return {
    signer: new WebCryptoSigner(identity, keys.signingJWK),
    decryptor: keys.encJWK ? new JwkDecryptor(keys.encJWK) : null,
  };
}

/**
 * A client for the unlocked identity. Returns null when locked — callers ask
 * the user to unlock rather than silently doing nothing.
 */
export function clientFor(identity) {
  const keyPair = signerFor(identity);
  if (!keyPair) return null;
  const relayUrl = relayUrlFor(identity);
  return new PoweurClient({
    relayUrl,
    signer: keyPair.signer,
    decryptor: keyPair.decryptor,
    sessionStore: new BrowserSessionStore(),
    resolve: resolveOptions(relayUrl),
  });
}

/** Registration, health and key publication — the calls that predate a signer. */
export function identityApiFor(relayUrl, options) {
  return new IdentityApi(relayUrl, options);
}

/**
 * Resolve options for a call with no signer behind it — the anonymous send
 * path (E15-T3), which resolves the *recipient's* relay and encryption key
 * without ever touching our own keys.
 */
export function resolveOptionsForRelay(relayUrl) {
  return resolveOptions(relayUrl);
}

/** `poweur identity lookup`, resolved web-first with the relay as a fallback host. */
export function lookup(identity, relayUrl) {
  return resolveIdentity(identity, resolveOptions(relayUrl));
}
