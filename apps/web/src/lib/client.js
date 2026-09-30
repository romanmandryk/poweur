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

import { PoweurClient, IdentityApi, ResolveCache, resolveIdentity, toBase64url } from "@poweur/client";
import { dohTxtResolver } from "@poweur/client/browser";

import { describeThisDevice, deviceHeadersFor } from "./devices.js";
import {
  BrowserSessionStore, getUnlockedKeys, isShellRuntime, loadIdentityRecord, relayUrlFor, resolveOptionsFor,
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

const DEVICE_FINGERPRINT_KEY = "poweur.device.fingerprint";

/** A random id for this browser or app install; the relay stores only its hash. */
function deviceFingerprint() {
  try {
    let fingerprint = localStorage.getItem(DEVICE_FINGERPRINT_KEY);
    if (!fingerprint) {
      fingerprint = toBase64url(crypto.getRandomValues(new Uint8Array(24)));
      localStorage.setItem(DEVICE_FINGERPRINT_KEY, fingerprint);
    }
    return fingerprint;
  } catch {
    return "";
  }
}

/**
 * Who this is in the owner's "Keys & devices" list. Optional and never fatal:
 * without storage the client is simply anonymous to the registry.
 */
function deviceHeaders(enrollmentId) {
  const fingerprint = deviceFingerprint();
  if (!fingerprint) return {};
  const info = describeThisDevice(globalThis.navigator?.userAgent ?? "", { native: isShellRuntime() });
  return deviceHeadersFor(fingerprint, info, enrollmentId);
}

/**
 * A client for the unlocked identity. Returns null when locked — callers ask
 * the user to unlock rather than silently doing nothing.
 */
export function clientFor(identity) {
  const keys = getUnlockedKeys();
  const keyPair = signerFor(identity);
  if (!keyPair) return null;
  const relayUrl = relayUrlFor(identity);
  // One client per unlocked identity: it keeps what it has read (message
  // history position, decrypted folders) between refreshes.
  // The enrollment id joins this device's registry row to its keystore
  // entry; it appears once the device is backed up, so it is part of the key.
  const enrollmentId = loadIdentityRecord(identity)?.enrollmentId ?? "";
  if (cachedClient && cachedClient.keys === keys && cachedClient.identity === identity && cachedClient.relayUrl === relayUrl && cachedClient.enrollmentId === enrollmentId) return cachedClient.client;
  const client = new PoweurClient({
    relayUrl,
    headers: deviceHeaders(enrollmentId),
    signer: keyPair.signer,
    decryptor: keyPair.decryptor,
    sessionStore: new BrowserSessionStore(),
    resolve: resolveOptions(relayUrl),
  });
  cachedClient = { keys, identity, relayUrl, enrollmentId, client };
  return client;
}
let cachedClient = null;

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
