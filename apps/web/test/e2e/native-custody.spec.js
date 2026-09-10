import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E19-T2: an identity created inside a shell is held by the OS keystore.
 *
 * The Swift and Kotlin halves cannot run in Chromium, but the decision they
 * exist to serve can: given a plugin, the app must stop reaching for a passkey
 * and put custody in the keystore instead — and must then be able to unlock
 * from it, with no PIN sheet anywhere in the flow.
 *
 * The fake below is the contract from the bottom of `js/native.js` and nothing
 * more, which is the point: if the app starts depending on something the real
 * plugin does not promise, this goes red.
 */
const MOBILE = { width: 375, height: 812 };

/**
 * Install a contract-shaped `PoweurKeystore` before any app code runs.
 *
 * Backed by `localStorage` on purpose: a real keystore outlives the app, and a
 * fake that forgot everything on reload would make "unlock after a relaunch" —
 * the whole point of hardware custody — untestable, while looking like an app
 * bug when it failed.
 */
async function stubKeystore(page, { available = true } = {}) {
  await page.addInitScript((biometricsAvailable) => {
    const STORE = "__fake_keystore";
    const read = () => { try { return JSON.parse(localStorage.getItem(STORE) ?? "{}"); } catch { return {}; } };
    const write = (all) => localStorage.setItem(STORE, JSON.stringify(all));

    window.__keystore = {
      prompts: [],
      get secrets() { return read(); },
    };
    globalThis.Capacitor = {
      isNativePlatform: () => true,
      Plugins: {
        PoweurKeystore: {
          async setSecret({ key, value, gate, reason }) {
            window.__keystore.prompts.push(reason);
            write({ ...read(), [key]: { value, gate } });
          },
          async getSecret({ key, reason }) {
            window.__keystore.prompts.push(reason);
            return { value: read()[key]?.value ?? null };
          },
          async deleteSecret({ key }) {
            const all = read();
            delete all[key];
            write(all);
          },
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

/** Claim a name. Native custody never asks for a PIN. */
async function claim(page, relay, handle) {
  await openApp(page, relay);
  if (await page.locator("#btn-welcome-start").count()) await page.click("#btn-welcome-start");
  if (await page.locator("#opt-create-new").count()) await page.click("#opt-create-new");
  await page.waitForSelector("#claim-card");
  await page.fill("#ni-handle", handle);
  await expect(page.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
  await page.click("#btn-claim");
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

    const identity = await claim(page, relay, `nat${Date.now().toString(36)}`);

    // Custody is the keystore's, the wrapped blob still lives in the record,
    // and the plugin holds exactly one secret — for this identity.
    const record = await page.evaluate((id) =>
      JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);
    expect(record.encryptedKeys.kdf).toBe("native");
    expect(record.encryptedKeys.gate).toBe("biometric");
    expect(record.encryptedKeys.ciphertext).toBeTruthy();

    const held = await page.evaluate(() => Object.keys(window.__keystore.secrets));
    expect(held).toEqual([`poweur.identity.${identity}`]);

    // The keys themselves never reach the plugin.
    const stored = await page.evaluate(() => JSON.stringify(window.__keystore.secrets));
    expect(stored).not.toContain(record.encryptedKeys.ciphertext);

    // Locking and unlocking goes through the keystore: the prompt names the
    // identity, and no PIN sheet appears.
    await page.evaluate(() => sessionStorage.clear());
    await page.reload();
    await expect(page.locator(".unlock-name")).toHaveText(identity.split(".")[0], { timeout: 20_000 });
    // `.unlock-name` appears on two screens: the landing's unlock card and the
    // `unlock` sub-page boot pushes when the session is gone. Whichever painted
    // first satisfied it, and under full-suite load that was sometimes the
    // card — whose `#btn-unlock-main` only *opens* the sub-page. The action
    // itself is always `#btn-do-unlock`, so get there first and then assert.
    if (await page.locator("#btn-unlock-main").count()) {
      await page.click("#btn-unlock-main");
    }
    const unlock = page.locator("#btn-do-unlock");
    await expect(unlock).toContainText("Unlock", { timeout: 20_000 });
    await unlock.click();
    await expect(page.locator(".dest-title")).toHaveText("Messages", { timeout: 30_000 });
    expect(await page.locator("#pin-input").count()).toBe(0);
    expect(await page.evaluate(() => window.__keystore.prompts))
      .toContain(`Unlock ${identity.split(".")[0]}`);
  });

  test("a shell with no enrolled biometrics uses a PRF passkey rather than failing closed on PIN", async ({ page }) => {
    test.slow();
    // A keystore that cannot gate on biometrics is not a keystore we will use:
    // storing behind an ungated secret would be a silent downgrade, so the app
    // takes the PRF passkey path instead.
    await stubPasskeys(page);
    await stubKeystore(page, { available: false });

    const identity = await claim(page, relay, `nof${Date.now().toString(36)}`);
    const record = await page.evaluate((id) =>
      JSON.parse(localStorage.getItem(`poweur:identity:${id}`)), identity);
    expect(record.encryptedKeys.kdf).toBe("prf");
    expect(await page.evaluate(() => Object.keys(window.__keystore.secrets).length)).toBe(0);
  });

  test("Add identity in the shell does not offer a passkey", async ({ page }) => {
    await stubPasskeys(page);
    await stubKeystore(page);
    await openApp(page, relay);
    await page.click("#opt-have-id");

    // Native custody: join an existing ID or create one. Passkeys are a
    // browser authenticator, and "Add new ID" is how the app claims a name
    // from this screen (the landing already has the claim field too).
    await expect(page.locator("#opt-join-device")).toBeVisible();
    await expect(page.locator("#opt-create-new")).toBeVisible();
    await expect(page.locator("#signin-id-input")).toHaveCount(0);
    await expect(page.locator("#btn-signin-passkey")).toHaveCount(0);
    await expect(page.locator("#btn-door-signin")).toHaveCount(0);
  });

  test("Keys & devices talks about this device, not this browser", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    await stubKeystore(page);
    await claim(page, relay, `key${Date.now().toString(36)}`);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-keys-devices");

    const panel = page.locator("#panel-root");
    await expect(panel.locator(".enrollment-row")).toHaveCount(1, { timeout: 20_000 });
    await expect(panel).toContainText("this device");
    await expect(panel).toContainText("the OS keystore");
    await expect(panel).not.toContainText("this browser");
    await expect(panel).not.toContainText("Back up this browser");
  });

  test("removing an identity forgets its hardware secret", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    await stubKeystore(page);
    const identity = await claim(page, relay, `rem${Date.now().toString(36)}`);
    expect(await page.evaluate(() => Object.keys(window.__keystore.secrets).length)).toBe(1);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-remove-id");
    await page.click("#panel-confirm-remove");

    // An orphaned keystore entry would sit there for the life of the install,
    // openable by nothing.
    await expect
      .poll(() => page.evaluate(() => Object.keys(window.__keystore.secrets).length), { timeout: 10_000 })
      .toBe(0);
    expect(await page.evaluate((id) =>
      localStorage.getItem(`poweur:identity:${id}`), identity)).toBeNull();
  });
});
