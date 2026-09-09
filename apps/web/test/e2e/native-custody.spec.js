import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E19-T2: an identity created inside a shell is held by the OS keystore.
 *
 * The Swift and Kotlin halves cannot run in Chromium, but the decision they
 * exist to serve can: given a plugin, the app must stop reaching for a passkey
 * or a PIN and put custody in the keystore instead — and must then be able to
 * unlock from it, with no PIN sheet anywhere in the flow.
 *
 * The fake below is the contract from the bottom of `js/native.js` and nothing
 * more, which is the point: if the app starts depending on something the real
 * plugin does not promise, this goes red.
 */
const MOBILE = { width: 375, height: 812 };

/** Install a contract-shaped `PoweurKeystore` before any app code runs. */
async function stubKeystore(page, { available = true } = {}) {
  await page.addInitScript((biometricsAvailable) => {
    const secrets = new Map();
    window.__keystore = { secrets, prompts: [] };
    globalThis.Capacitor = {
      isNativePlatform: () => true,
      Plugins: {
        PoweurKeystore: {
          async setSecret({ key, value, gate, reason }) {
            window.__keystore.prompts.push(reason);
            secrets.set(key, { value, gate });
          },
          async getSecret({ key, reason }) {
            window.__keystore.prompts.push(reason);
            return { value: secrets.get(key)?.value ?? null };
          },
          async deleteSecret({ key }) { secrets.delete(key); },
          async canUseBiometrics() {
            return biometricsAvailable
              ? { available: true, kind: "face" }
              : { available: false, reason: "not_enrolled" };
          },
        },
      },
    };
  }, available);
}

/** Claim a name; `pin` says whether a PIN sheet is expected on the way. */
async function claim(page, relay, handle, { pin }) {
  await openApp(page, relay);
  if (await page.locator("#btn-welcome-start").count()) await page.click("#btn-welcome-start");
  if (await page.locator("#opt-create-new").count()) await page.click("#opt-create-new");
  await page.waitForSelector("#claim-card");
  await page.fill("#ni-handle", handle);
  await expect(page.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
  await page.click("#btn-claim");
  if (pin) {
    await page.waitForSelector("#pin-input");
    await page.fill("#pin-input", "test-pin");
    await page.fill("#pin-confirm", "test-pin");
    await page.click("#btn-pin-ok");
  }
  await page.waitForSelector("#btn-onboard-skip", { timeout: 45_000 });
  await page.click("#btn-onboard-skip");
  await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });
  return `${handle}.poweur.net`;
}

test.describe("native custody", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("a keystore takes custody, and unlocking never asks for a PIN", async ({ page }) => {
    test.slow();
    // Passkeys are stubbed as *available* on purpose: the claim being made is
    // that the keystore wins over a working passkey, not that it fills a gap.
    await stubPasskeys(page);
    await stubKeystore(page);

    const identity = await claim(page, relay, `nat${Date.now().toString(36)}`, { pin: false });

    // Custody is the keystore's, the wrapped blob still lives in the record,
    // and the plugin holds exactly one secret — for this identity.
    const record = await page.evaluate((id) =>
      JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);
    expect(record.encryptedKeys.kdf).toBe("native");
    expect(record.encryptedKeys.gate).toBe("biometric");
    expect(record.encryptedKeys.ciphertext).toBeTruthy();

    const held = await page.evaluate(() => [...window.__keystore.secrets.keys()]);
    expect(held).toEqual([`poweur.identity.${identity}`]);

    // The keys themselves never reach the plugin.
    const stored = await page.evaluate(() =>
      JSON.stringify([...window.__keystore.secrets.values()]));
    expect(stored).not.toContain(record.encryptedKeys.ciphertext);

    // Locking and unlocking goes through the keystore: the prompt names the
    // identity, and no PIN sheet appears.
    await page.evaluate(() => sessionStorage.clear());
    await page.reload();
    await expect(page.locator(".unlock-name")).toHaveText(identity.split(".")[0], { timeout: 20_000 });
    await expect(page.locator("#btn-unlock-main")).toContainText("Unlock");

    await page.click("#btn-unlock-main");
    await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 30_000 });
    expect(await page.locator("#pin-input").count()).toBe(0);
    expect(await page.evaluate(() => window.__keystore.prompts))
      .toContain(`Unlock ${identity.split(".")[0]}`);
  });

  test("a shell with no enrolled biometrics falls back rather than failing", async ({ page }) => {
    test.slow();
    // A keystore that cannot gate on biometrics is not a keystore we will use:
    // storing behind an ungated secret would be a silent downgrade, so the app
    // takes the PIN path and says nothing was lost.
    await stubPasskeys(page);
    await stubKeystore(page, { available: false });

    const identity = await claim(page, relay, `nof${Date.now().toString(36)}`, { pin: true });
    const record = await page.evaluate((id) =>
      JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);
    expect(record.encryptedKeys.kdf).not.toBe("native");
    expect(await page.evaluate(() => window.__keystore.secrets.size)).toBe(0);
  });

  test("removing an identity forgets its hardware secret", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    await stubKeystore(page);
    const identity = await claim(page, relay, `rem${Date.now().toString(36)}`, { pin: false });
    expect(await page.evaluate(() => window.__keystore.secrets.size)).toBe(1);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-remove-id");
    await page.click("#panel-confirm-remove");

    // An orphaned keystore entry would sit there for the life of the install,
    // openable by nothing.
    await expect
      .poll(() => page.evaluate(() => window.__keystore.secrets.size), { timeout: 10_000 })
      .toBe(0);
    expect(await page.evaluate((id) =>
      localStorage.getItem(`poweur:identity:${id}`), identity)).toBeNull();
  });
});
