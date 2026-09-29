import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { appPath } from "../helpers/app-path.mjs";

// An identity page's "Message" link lands on the app with ?to=; once the
// visitor's keys are open, the chat with that ID is on screen.
test.describe("identity page call to action", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => {
    relay = await startRelay();
  });

  test.afterAll(() => {
    relay?.stop();
  });

  test("?to= opens the chat and leaves the address bar clean", async ({ page }) => {
    await stubPasskeys(page);
    const handle = `cta${Date.now().toString(36)}`;
    await registerIdentity(page, relay, handle);

    await page.goto(`${relay.baseUrl}${appPath()}?to=bob.poweur.net`);
    // Keys are locked after a navigation; the chat waits for the unlock.
    await page.click("#btn-unlock-main");
    await page.locator("#btn-do-unlock").click({ timeout: 30_000 });
    await expect(page.locator("#thread-input")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("body")).toContainText(/bob\s*No messages yet/);
    expect(new URL(page.url()).searchParams.get("to")).toBeNull();
  });
});
