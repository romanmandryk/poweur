import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";

/**
 * E15-T1 acceptance: the five destinations render and route, and the shell is
 * usable at a 375px viewport.
 *
 * Runs against a real relay with a real registered identity — the destinations
 * that need keys behave differently when locked, and a stubbed app would not
 * catch that.
 */
const MOBILE = { width: 375, height: 667 };

const DESTINATIONS = [
  { page: "messages", title: "Messages" },
  { page: "contacts", title: "Contacts" },
  { page: "files", title: "Files" },
  { page: "settings", title: "Settings" },
];

/**
 * Stand in for a platform authenticator.
 *
 * Headless Chromium has no real one, but a stub that returns nothing is not a
 * useful stand-in either: without `getPublicKey()` the relay has nothing to
 * verify, so the app declines to enroll and half of EPIC-011 goes untested.
 * This one holds a real Ed25519 key and signs real assertions. It reports no
 * PRF, so registration takes the PIN path — which is also the case worth
 * covering, since a PIN-wrapped browser cannot bootstrap itself.
 */
async function stubPasskeys(page) {
  await page.addInitScript(() => {
    const rawId = crypto.getRandomValues(new Uint8Array(32));
    let keyPair = null;

    const ensureKey = async () => {
      keyPair ??= await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
      return keyPair;
    };

    const base = {
      rawId: rawId.buffer,
      id: btoa(String.fromCharCode(...rawId)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""),
      type: "public-key",
      getClientExtensionResults: () => ({}), // no PRF → PIN path
    };

    navigator.credentials.create = async () => {
      const pair = await ensureKey();
      const spki = await crypto.subtle.exportKey("spki", pair.publicKey);
      return {
        ...base,
        response: {
          getPublicKey: () => spki,
          getPublicKeyAlgorithm: () => -8, // COSE EdDSA
        },
      };
    };

    navigator.credentials.get = async (options) => {
      const pair = await ensureKey();
      const challenge = new Uint8Array(options.publicKey.challenge);
      const b64url = (bytes) =>
        btoa(String.fromCharCode(...new Uint8Array(bytes)))
          .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

      const clientData = new TextEncoder().encode(JSON.stringify({
        type: "webauthn.get",
        challenge: b64url(challenge),
        origin: window.location.origin,
      }));
      const rpHash = new Uint8Array(
        await crypto.subtle.digest("SHA-256", new TextEncoder().encode(window.location.hostname)),
      );
      const authData = new Uint8Array(37);
      authData.set(rpHash, 0);
      authData[32] = 0x01 | 0x04;
      authData[36] = 1;
      const clientHash = new Uint8Array(await crypto.subtle.digest("SHA-256", clientData));
      const signed = new Uint8Array(authData.length + clientHash.length);
      signed.set(authData, 0);
      signed.set(clientHash, authData.length);
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

/** Register through the UI, exactly as a new user would. */
async function createIdentity(page, relay) {
  const handle = `dest${Date.now().toString(36)}`;
  await page.goto(`${relay.baseUrl}/app/`);
  await page.evaluate((url) => {
    localStorage.setItem("poweur:config", JSON.stringify({ relayUrl: url, parentDomain: "poweur.net" }));
  }, relay.baseUrl);
  await page.reload();

  if (await page.locator("#btn-welcome-start").count()) await page.click("#btn-welcome-start");
  if (await page.locator("#opt-create-new").count()) await page.click("#opt-create-new");
  await page.waitForSelector("#claim-card");
  await page.fill("#ni-handle", handle);
  await expect(page.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
  await page.click("#btn-claim");
  await expect(page.locator("#pin-input")).toBeVisible({ timeout: 30_000 });
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

test.describe("five destinations at 375px", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("routes between all five and never scrolls sideways", async ({ page }) => {
    await stubPasskeys(page);
    await createIdentity(page, relay);

    await expect(page.locator(".nav-tab")).toHaveCount(5);

    for (const { page: destination, title } of DESTINATIONS) {
      await page.click(`.nav-tab[data-page="${destination}"]`);
      await expect(page.locator(".dest-title")).toHaveText(title);
      await expect(page.locator(`.nav-tab[data-page="${destination}"]`)).toHaveAttribute("aria-selected", "true");

      // A wrapped shell (EPIC-019) has nowhere to put a horizontal scrollbar.
      const overflows = await page.evaluate(() =>
        document.documentElement.scrollWidth > document.documentElement.clientWidth);
      expect(overflows, `${destination} overflows horizontally`).toBe(false);

      // The header title must survive alongside its action button — an unsized
      // inline SVG in that button once collapsed the title to zero width.
      // Polled, because a destination may re-render when its data lands and a
      // node measured across that swap reports zero.
      await expect
        .poll(
          () => page.evaluate(() => document.querySelector(".dest-title")?.getBoundingClientRect().width ?? 0),
          { message: `${destination} title collapsed` },
        )
        .toBeGreaterThan(60);
    }

    // The fifth tab is Apps. It used to render "New identity — step 1 of 2":
    // the create-identity form was the launcher destination, so someone who
    // already had an identity was offered another one. Claiming moved to the
    // front door (E15-T7), and what is left here says so (E15-T11).
    await page.click('.nav-tab[data-page="launcher"]');
    await expect(page.locator(".dest-title")).toHaveText("Apps");
    await expect(page.locator("#ni-handle")).toHaveCount(0);
  });

  test("every nav tab clears the 44px touch-target floor", async ({ page }) => {
    await stubPasskeys(page);
    await createIdentity(page, relay);

    // Measured in one pass, and only once the shell has settled: it re-renders
    // from strings whenever a background read lands, so a measurement taken
    // across that swap sees detached nodes or a half-built nav. Under a full
    // suite — several relays, slower registration — that window is wide enough
    // to hit, which is what made this flake.
    const measure = () => page.$$eval(".nav-tab", (tabs) =>
      tabs.map((tab) => {
        const rect = tab.getBoundingClientRect();
        return { height: rect.height, width: rect.width };
      }));
    // The whole check polls, not just the wait for five tabs: a measurement
    // taken while the shell is mid-render sees a nav that is present but not
    // yet laid out, and asserting on that once made this test flake under a
    // full-suite load rather than report a real regression.
    await expect
      .poll(async () => {
        const boxes = await measure();
        return boxes.length === 5 && boxes.every((box) => box.height >= 44 && box.width >= 44);
      }, { message: "the bottom nav never settled at five tabs of at least 44px" })
      .toBe(true);
  });

  test("Messages shows three trays and switches between them", async ({ page }) => {
    await stubPasskeys(page);
    await createIdentity(page, relay);

    await expect(page.locator(".tray-tab")).toHaveCount(3);
    await expect(page.locator(".tray-tab.active")).toHaveText("Inbox");
    await expect(page.locator(".empty-state-title")).toHaveText("No messages yet");

    await page.click('.tray-tab[data-tray="requests"]');
    await expect(page.locator(".empty-state-title")).toHaveText("No contact requests");

    await page.click('.tray-tab[data-tray="anonymous"]');
    await expect(page.locator(".empty-state-title")).toHaveText("No anonymous messages");

    // No badges when nothing is waiting: a badge that never clears teaches
    // people to ignore badges (E07-T3).
    await expect(page.locator(".tray-badge")).toHaveCount(0);
  });

  test("Contacts offers the identity input, which rejects a typo", async ({ page }) => {
    await stubPasskeys(page);
    await createIdentity(page, relay);

    await page.click('.nav-tab[data-page="contacts"]');
    await expect(page.locator(".empty-state-title")).toHaveText("No contacts yet");

    await page.click("#btn-add-contact-empty");
    const field = page.locator(".idin input");
    await expect(field).toBeVisible();

    await field.fill("not an identity");
    await expect(page.locator(".idin-status")).toContainText("does not look like a Poweur ID");

    // Pressing send on a typo resolves it first and refuses — the button is
    // live rather than disabled, because a control that greys out while a
    // debounced lookup is in flight leaves someone who typed a name and
    // pressed the button they were looking at with nothing at all.
    await page.click("#btn-add-contact-go");
    await expect(page.locator(".toast.warning")).toContainText("Enter a Poweur ID we can find");
    // Nothing was written: the panel is still open on the same typo.
    await expect(field).toHaveValue("not an identity");
  });

  test("Contacts completes a bare handle with your own domain", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await createIdentity(page, relay);

    await page.click('.nav-tab[data-page="contacts"]');
    await page.click("#btn-add-contact-empty");
    // A hosted relay puts everyone under one domain, so this is what people
    // type; before, it failed validation with "that does not look like a
    // Poweur ID", which is true and useless.
    await page.locator(".idin input").fill(identity.split(".")[0]);
    await expect(page.locator(".idin-status")).toContainText(`Found ${identity}`, { timeout: 20_000 });
  });

  test("Files lists the storage roots for the unlocked identity", async ({ page }) => {
    await stubPasskeys(page);
    await createIdentity(page, relay);

    await page.click('.nav-tab[data-page="files"]');
    await expect(page.locator(".conv-name").first()).toBeVisible({ timeout: 20_000 });

    const names = await page.locator(".conv-name").allInnerTexts();
    expect(names.join(" ")).toContain("public");
    expect(names.join(" ")).toContain("private");
  });

  test("Settings lists this device from the relay keystore", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await createIdentity(page, relay);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-keys-devices");

    const panel = page.locator("#panel-root");
    // Registration enrolls this browser, so the inventory is populated from
    // the relay rather than from anything held locally.
    await expect(panel.locator(".enrollment-row")).toHaveCount(1);
    await expect(panel).toContainText("this device");
    await expect(panel).toContainText("PIN"); // the wrap this stubbed passkey used
    // You cannot evict the device you are on.
    await expect(panel.locator("[data-remove-enrollment]")).toBeDisabled();

    expect(identity).toContain(".poweur.net");
  });

  test("shows a real 24-word recovery kit and checks it back", async ({ page }) => {
    await stubPasskeys(page);
    await createIdentity(page, relay);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-recovery-kit");

    // The stubbed authenticator has no PRF, so registration fell back to a PIN.
    // The identity is still seed-derived, so the kit itself is available.
    const panel = page.locator("#panel-root");
    await expect(panel.locator(".mnemonic-word")).toHaveCount(24);

    // And the kit verifies against itself.
    const words = await panel.locator(".mnemonic-word").allInnerTexts();
    await panel.locator("#btn-verify-kit").click();
    await panel.locator("#kit-input").fill(words.join(" "));
    await panel.locator("#btn-check-kit").click();
    await expect(panel.locator("#kit-result")).toContainText("That's your kit");

    await panel.locator("#kit-input").fill(["abandon"].concat(words.slice(1)).join(" "));
    await panel.locator("#btn-check-kit").click();
    await expect(panel.locator("#kit-result")).toContainText("doesn't match");
  });
});
