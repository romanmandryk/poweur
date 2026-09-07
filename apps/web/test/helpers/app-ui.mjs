/**
 * Driving the SPA from Playwright: a stand-in platform authenticator and a
 * registration that goes through the real screens.
 *
 * Headless Chromium has no authenticator, and a stub that returns nothing is
 * not a useful stand-in either — without `getPublicKey()` the app declines to
 * enroll (EPIC-011), so half the flows never start. This one holds a real
 * Ed25519 key and signs real assertions, and reports no PRF so registration
 * takes the PIN path.
 */

import { expect } from "@playwright/test";

export async function stubPasskeys(page) {
  await page.addInitScript(() => {
    const rawId = crypto.getRandomValues(new Uint8Array(32));
    let keyPair = null;
    const ensureKey = async () => {
      keyPair ??= await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
      return keyPair;
    };
    const b64url = (bytes) =>
      btoa(String.fromCharCode(...new Uint8Array(bytes)))
        .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    const base = {
      rawId: rawId.buffer,
      id: b64url(rawId),
      type: "public-key",
      getClientExtensionResults: () => ({}), // no PRF → PIN path
    };
    navigator.credentials.create = async () => {
      const pair = await ensureKey();
      const spki = await crypto.subtle.exportKey("spki", pair.publicKey);
      return { ...base, response: { getPublicKey: () => spki, getPublicKeyAlgorithm: () => -8 } };
    };
    navigator.credentials.get = async (options) => {
      const pair = await ensureKey();
      const clientData = new TextEncoder().encode(JSON.stringify({
        type: "webauthn.get",
        challenge: b64url(new Uint8Array(options.publicKey.challenge)),
        origin: window.location.origin,
      }));
      const rpHash = new Uint8Array(await crypto.subtle.digest(
        "SHA-256", new TextEncoder().encode(window.location.hostname)));
      const authData = new Uint8Array(37);
      authData.set(rpHash, 0);
      authData[32] = 0x05;
      authData[36] = 1;
      const clientHash = new Uint8Array(await crypto.subtle.digest("SHA-256", clientData));
      const signed = new Uint8Array(69);
      signed.set(authData, 0);
      signed.set(clientHash, 37);
      const signature = await crypto.subtle.sign({ name: "Ed25519" }, pair.privateKey, signed);
      return {
        ...base,
        response: {
          clientDataJSON: clientData.buffer,
          authenticatorData: authData.buffer,
          signature,
        },
      };
    };
    if (window.PublicKeyCredential) {
      PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable = async () => true;
    }
  });
}

/** Open the SPA pointed at a test relay (the relay URL is config, not origin). */
export async function openApp(page, relay) {
  await page.goto(`${relay.baseUrl}/app/`);
  await page.evaluate((url) => {
    localStorage.setItem("poweur:config", JSON.stringify({ relayUrl: url, parentDomain: "poweur.net" }));
  }, relay.baseUrl);
  await page.reload();
}

/** Register a hosted identity through the real screens. Returns its FQDN. */
export async function registerIdentity(page, relay, handle) {
  await openApp(page, relay);
  if (await page.locator("#btn-welcome-start").count()) await page.click("#btn-welcome-start");
  if (await page.locator("#opt-create-new").count()) await page.click("#opt-create-new");
  await page.fill("#ni-handle", handle);
  await page.fill("#ni-domain", "poweur.net");
  await page.click("#btn-next-id");
  await page.waitForSelector("#btn-create-id");
  await page.click("#btn-create-id");
  await page.waitForSelector("#pin-input");
  await page.fill("#pin-input", "test-pin");
  await page.fill("#pin-confirm", "test-pin");
  await page.click("#btn-pin-ok");
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
  return `${handle}.poweur.net`;
}
