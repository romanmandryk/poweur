import { readFileSync } from "node:fs";

import { test, expect } from "@playwright/test";
import { SDK_VERSION } from "@poweur/client";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { appBuildInfo, isNextApp } from "../helpers/app-path.mjs";

// The app under test reports its own version (legacy js/build-info.js or web-next's).
const { APP_VERSION, APP_BUILD_TIME } = appBuildInfo();

/** The relay's semver, read from the one place it is bumped. */
const RELAY_VERSION = readFileSync(new URL("../../../api/internal/buildinfo/buildinfo.go", import.meta.url), "utf8")
  .match(/var Version = "([^"]+)"/)[1];

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

function destHandle() {
  return `dest${Date.now().toString(36)}`;
}

test.describe("five destinations at 375px", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("routes between all five and never scrolls sideways", async ({ page }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, destHandle());

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
    await registerIdentity(page, relay, destHandle());

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

  test("Messages shows its trays and switches between them", async ({ page }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, destHandle());

    // The rewrite offers the anonymous tray only to an inbox that accepts anonymous messages.
    await expect(page.locator(".tray-tab")).toHaveCount(isNextApp() ? 2 : 3);
    await expect(page.locator(".tray-tab.active")).toHaveText("Inbox");
    await expect(page.locator(".empty-state-title")).toHaveText("No messages yet");

    await page.click('.tray-tab[data-tray="requests"]');
    await expect(page.locator(".empty-state-title")).toHaveText("No contact requests");

    if (!isNextApp()) {
      await page.click('.tray-tab[data-tray="anonymous"]');
      await expect(page.locator(".empty-state-title")).toHaveText("No anonymous messages");
    }

    // No badges when nothing is waiting: a badge that never clears teaches
    // people to ignore badges (E07-T3).
    await expect(page.locator(".tray-badge")).toHaveCount(0);
  });

  test("Contacts offers the identity input, which rejects a typo", async ({ page }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, destHandle());

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
    const identity = await registerIdentity(page, relay, destHandle());

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
    await registerIdentity(page, relay, destHandle());

    await page.click('.nav-tab[data-page="files"]');
    await expect(page.locator(".conv-name").first()).toBeVisible({ timeout: 20_000 });

    const names = await page.locator(".conv-name").allInnerTexts();
    expect(names.join(" ")).toContain("public");
    expect(names.join(" ")).toContain("private");
  });

  test("Settings lists this device from the relay keystore", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await registerIdentity(page, relay, destHandle());

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-keys-devices");

    const panel = page.locator("#panel-root");
    // Registration enrolls this browser, so the inventory is populated from
    // the relay rather than from anything held locally.
    await expect(panel.locator(".enrollment-row")).toHaveCount(1);
    await expect(panel).toContainText("this device");
    await expect(panel).toContainText("passkey (PRF)");
    // You cannot evict the device you are on.
    await expect(panel.locator("[data-remove-enrollment]")).toBeDisabled();

    expect(identity).toContain(".poweur.net");
  });

  test("About lists app, SDK and connected relay versions", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await registerIdentity(page, relay, destHandle());

    await page.click('.nav-tab[data-page="settings"]');
    // From the sources the versions are bumped in, so a patch bump does not
    // break the screen that reports it.
    await expect(page.locator("#about-app-version")).toHaveText(APP_VERSION);
    await expect(page.locator("#about-sdk-version")).toHaveText(SDK_VERSION);
    await expect(page.locator("#about-app-build")).toContainText(APP_BUILD_TIME.slice(0, 10));
    await expect(page.locator("#about-relay-version")).toHaveText(RELAY_VERSION, { timeout: 15_000 });
    await expect(page.locator("#about-relay-meta")).toContainText(identity);
    await expect(page.locator("#about-relay-meta")).toContainText(relay.baseUrl);
  });

  test("shows a real 24-word recovery kit and checks it back", async ({ page }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, destHandle());

    await page.click('.nav-tab[data-page="settings"]');
    await page.click("#row-recovery-kit");

    // The identity is seed-derived, so the kit itself is available.
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
