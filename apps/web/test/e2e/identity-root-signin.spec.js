import { test, expect, chromium } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * EPIC-028 E28-T14: the identity's own page offers "Sign in as <id>" to a
 * browser that has never used the ID, and hides it from the owner's browser.
 * The page lives on the identity's host, so this browser maps *.poweur.net to
 * the test relay.
 */
test.describe("identity root: sign in as the owner", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;
  /** @type {import("@playwright/test").Browser} */
  let mapped;

  test.beforeAll(async () => {
    relay = await startRelay();
    mapped = await chromium.launch({ args: ["--host-resolver-rules=MAP *.poweur.net 127.0.0.1"] });
  });

  test.afterAll(async () => {
    await mapped?.close();
    relay?.stop();
  });

  test("a fresh browser gets the link, the owner's browser does not, a visitor's keeps it", async ({ page }) => {
    await stubPasskeys(page);
    const identity = await registerIdentity(page, relay, `root${Date.now().toString(36)}`);
    const home = `http://${identity}:${relay.port}/`;

    const context = await mapped.newContext();
    const visitor = await context.newPage();
    await visitor.goto(home);
    const link = visitor.locator("#btn-signin-as");
    await expect(link).toHaveText(`Sign in as ${identity}`);
    await expect(link).toHaveAttribute("href", "/id/");
    await expect(visitor.locator("#btn-inbox")).toHaveCount(0);

    // The owner's browser: the web app wrote poweur_ids, so the page opens the inbox instead.
    await context.addCookies([{ name: "poweur_ids", value: identity, domain: ".poweur.net", path: "/" }]);
    await visitor.reload();
    await expect(visitor.locator("#btn-inbox")).toBeVisible();
    await expect(visitor.locator("#btn-signin-as")).toHaveCount(0);

    // Someone else's browser (bob): message as bob, and the owner's way in stays.
    await context.addCookies([{ name: "poweur_ids", value: "bob.poweur.net", domain: ".poweur.net", path: "/" }]);
    await visitor.reload();
    await expect(visitor.locator("#btn-message")).toContainText("Message as bob.poweur.net");
    await expect(visitor.locator("#btn-signin-as")).toBeVisible();

    // Following it lands in the identity's own web app.
    await visitor.click("#btn-signin-as");
    await expect(visitor).toHaveURL(new RegExp(`^http://${identity.replace(/\./g, "\\.")}:${relay.port}/id/`));
    await context.close();
  });
});
