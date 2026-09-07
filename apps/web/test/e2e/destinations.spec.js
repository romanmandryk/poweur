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

async function stubPasskeys(page) {
  await page.addInitScript(() => {
    const fakeId = crypto.getRandomValues(new Uint8Array(32));
    class FakeCredential {
      constructor() {
        this.rawId = fakeId.buffer;
        this.id = btoa(String.fromCharCode(...fakeId));
        this.type = "public-key";
      }
      getClientExtensionResults() { return {}; } // no PRF → PIN path
    }
    navigator.credentials.create = async () => new FakeCredential();
    navigator.credentials.get = async () => new FakeCredential();
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
  await page.fill("#ni-handle", handle);
  await page.fill("#ni-domain", "poweur.net");
  await page.check("#ni-hosted-step1");
  await page.click("#btn-next-id");
  await expect(page.locator("#btn-create-id")).toBeVisible({ timeout: 10_000 });
  await page.check("#ni-hosted");
  await page.click("#btn-create-id");
  await expect(page.locator("#pin-input")).toBeVisible({ timeout: 30_000 });
  await page.fill("#pin-input", "test-pin");
  await page.fill("#pin-confirm", "test-pin");
  await page.click("#btn-pin-ok");
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

    // The launcher is the fifth tab and keeps its own content.
    await page.click('.nav-tab[data-page="launcher"]');
    await expect(page.locator("#ni-handle, #btn-create-id")).toHaveCount(1);
  });

  test("every nav tab clears the 44px touch-target floor", async ({ page }) => {
    await stubPasskeys(page);
    await createIdentity(page, relay);

    for (const tab of await page.locator(".nav-tab").all()) {
      const box = await tab.boundingBox();
      expect(box.height, "tab height").toBeGreaterThanOrEqual(44);
      expect(box.width, "tab width").toBeGreaterThanOrEqual(44);
    }
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
    // Resolution gates the action, so a typo cannot be submitted.
    await expect(page.locator("#btn-add-contact-go")).toBeDisabled();
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

  test("Settings shows the EPIC-011 keys surface, honestly labelled", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await createIdentity(page, relay);

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-keys-devices");

    const panel = page.locator("#panel-root");
    await expect(panel).toContainText("Preview");
    await expect(panel).toContainText("EPIC-011");
    await expect(panel.locator(".enrollment-row")).toHaveCount(1);
    await expect(panel).toContainText("this device");
    await expect(panel).toContainText("PIN"); // the wrap actually used

    // The write path refuses rather than pretending.
    await panel.locator("#btn-enroll-device").click();
    await expect(page.locator("#toast-root")).toContainText("has not shipped yet");

    expect(identity).toContain(".poweur.net");
  });
});
