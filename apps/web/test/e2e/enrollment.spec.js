import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";

/**
 * The two-device enrollment ceremony, end to end (EPIC-011 E11-T3).
 *
 * Two browser contexts stand in for two devices, because that is the only way
 * to test what the ceremony is actually for: the new one holds no key at all,
 * and everything it ends up with has to arrive through the relay as ciphertext
 * the relay cannot read.
 *
 * The security property under test is the number comparison. The six digits
 * are derived independently on both sides from the ephemeral public key, so
 * they matching is what proves the two screens are talking to each other
 * rather than to an interloper.
 */

/** A stand-in platform authenticator with a real Ed25519 key. */
async function stubPasskeys(page) {
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
      getClientExtensionResults: () => ({}),
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
      return { ...base, response: { clientDataJSON: clientData.buffer, authenticatorData: authData.buffer, signature } };
    };
    if (window.PublicKeyCredential) {
      PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable = async () => true;
    }
  });
}

async function openApp(page, relay) {
  await page.goto(`${relay.baseUrl}/app/`);
  await page.evaluate((url) => {
    localStorage.setItem("poweur:config", JSON.stringify({ relayUrl: url, parentDomain: "poweur.net" }));
  }, relay.baseUrl);
  await page.reload();
}

async function registerIdentity(page, relay, handle) {
  await openApp(page, relay);
  await page.click("#btn-welcome-start");
  await page.click("#opt-create-new");
  await page.fill("#ni-handle", handle);
  await page.fill("#ni-domain", "poweur.net");
  await page.click("#btn-next-id");
  await page.waitForSelector("#btn-create-id");
  await page.click("#btn-create-id");
  await page.waitForSelector("#pin-input");
  await page.fill("#pin-input", "test-pin");
  await page.fill("#pin-confirm", "test-pin");
  await page.click("#btn-pin-ok");
  await page.waitForSelector("#btn-onboard-skip", { timeout: 45_000 });
  // First run lands in the setup flow (E15-T5); these specs test what comes
  // after it, and onboarding has its own coverage.
  await page.click("#btn-onboard-skip");
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
  return `${handle}.poweur.net`;
}

test.describe("new-device enrollment", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test("a second device joins, and both show the same six digits", async ({ browser }) => {
    const laptop = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const phone = await browser.newContext({ viewport: { width: 375, height: 812 } });
    const laptopPage = await laptop.newPage();
    const phonePage = await phone.newPage();
    await stubPasskeys(laptopPage);
    await stubPasskeys(phonePage);

    const identity = await registerIdentity(laptopPage, relay, `enr${Date.now().toString(36)}`);

    // ── The new device opens a rendezvous and shows its codes ───────────────
    await openApp(phonePage, relay);
    await phonePage.click("#btn-welcome-start");
    await phonePage.click("#opt-join-device");
    await phonePage.fill("#join-identity", identity);
    await phonePage.click("#btn-join-start");

    const requestCode = await phonePage.locator(".rendezvous-code").innerText();
    const phoneSas = await phonePage.locator(".sas-code").innerText();
    expect(requestCode.trim()).not.toBe("");
    expect(phoneSas.trim()).toMatch(/^\d{6}$/);

    // ── The trusted device looks it up and sees the *same* digits ───────────
    await laptopPage.click('.nav-tab[data-page="settings"]');
    await laptopPage.click("#row-keys-devices");
    await laptopPage.click("#btn-enroll-device");
    await laptopPage.fill("#enroll-rendezvous", requestCode.trim());
    await laptopPage.click("#btn-enroll-lookup");

    const laptopSas = await laptopPage.locator(".sas-code").innerText();
    // Derived independently on both sides from the ephemeral key — this
    // equality *is* the authentication step.
    expect(laptopSas.trim()).toBe(phoneSas.trim());

    // ── Approve; the phone claims the seed and sets itself up ───────────────
    await laptopPage.click("#btn-enroll-approve");

    // The seed arrives, then the phone wraps it under its own credential. This
    // stub reports no PRF, so that means a PIN — the key material is never
    // stored as it arrived.
    await expect(phonePage.locator("#pin-input")).toBeVisible({ timeout: 60_000 });
    await phonePage.fill("#pin-input", "phone-pin");
    await phonePage.fill("#pin-confirm", "phone-pin");
    await phonePage.click("#btn-pin-ok");

    await expect(phonePage.locator(".dest-title")).toHaveText("Messages", { timeout: 60_000 });

    // The phone now holds the same identity, derived from the same seed.
    const phoneRecord = await phonePage.evaluate((id) =>
      JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);
    const laptopRecord = await laptopPage.evaluate((id) =>
      JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);

    expect(phoneRecord.identity).toBe(identity);
    expect(phoneRecord.publicKey).toBe(laptopRecord.publicKey);
    expect(phoneRecord.encPublicKey).toBe(laptopRecord.encPublicKey);
    expect(phoneRecord.seedDerived).toBe(true);
    // A separate enrollment, because it wrapped the seed under its own passkey.
    expect(phoneRecord.enrollmentId).not.toBe(laptopRecord.enrollmentId);

    // ── The laptop's inventory now lists two devices ────────────────────────
    // No reload: unlocked keys are memory-only by design, so a reload would
    // just send this device back to the unlock screen.
    await laptopPage.click("#row-keys-devices");
    await expect(laptopPage.locator("#panel-root .enrollment-row")).toHaveCount(2);
    await expect(laptopPage.locator("#panel-root")).toContainText("this device");

    await laptop.close();
    await phone.close();
  });

  test("a wrong request code is refused rather than half-approved", async ({ page }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, `bad${Date.now().toString(36)}`);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-keys-devices");
    await page.click("#btn-enroll-device");
    await page.fill("#enroll-rendezvous", "not-a-real-rendezvous-id");
    await page.click("#btn-enroll-lookup");

    await expect(page.locator("#toast-root")).toContainText("No pending device");
    // Nothing to approve — the confirmation step never appears.
    await expect(page.locator("#btn-enroll-approve")).toHaveCount(0);
  });
});
