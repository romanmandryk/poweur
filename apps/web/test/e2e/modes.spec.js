import { test, expect } from "@playwright/test";
import { appPath } from "../helpers/app-path.mjs";
import { startRelay } from "../helpers/relay.mjs";
import { stubPasskeys, registerIdentity } from "../helpers/app-ui.mjs";

/**
 * E15-T7/T8/T9 — one static tree, three front doors.
 *
 * The relay is a single listener on 127.0.0.1 with no DNS, so the hosts are
 * mapped into it with Chromium's own resolver rules. That is enough: what is
 * under test is what the app does with the `Host` it is served under, and the
 * relay routes on exactly that header in production too.
 *
 * One thing these hosts cannot do is *create* an identity. `http://id.poweur.net`
 * is not a secure context, so `crypto.subtle` and `PublicKeyCredential` do not
 * exist there, and Chromium's `--unsafely-treat-insecure-origin-as-secure` no
 * longer grants it. So claims here are made through `127.0.0.1`, which is
 * trustworthy by name, and what these tests assert is the *door* each host
 * shows. Production serves all of them over https, where the question does not
 * arise.
 */
const MOBILE = { width: 375, height: 812 };

/** @type {Awaited<ReturnType<typeof startRelay>>} */
let relay;
let port;

test.beforeAll(async () => {
  relay = await startRelay();
  port = new URL(relay.baseUrl).port;
});
test.afterAll(() => relay?.stop());

test.use({
  viewport: MOBILE,
  launchOptions: {
    args: ["--host-resolver-rules=MAP *.poweur.net 127.0.0.1,MAP poweur.net 127.0.0.1"],
  },
});

const at = (host, path = appPath()) => `http://${host}:${port}${path}`;

test.describe("front doors", () => {
  test("the launcher host lands on the claim field, not a welcome card", async ({ page }) => {
    await stubPasskeys(page);
    await page.goto(at("id.poweur.net"));

    await expect(page.locator(".landing-title")).toBeVisible({ timeout: 20_000 });
    await expect(page.locator("#claim-card")).toBeVisible();
    // The domain is the host's answer, shown as a suffix rather than asked.
    await expect(page.locator("#ni-domain-fixed")).toHaveText(".poweur.net");
    // E15-T10: neither question survives.
    await expect(page.locator("#ni-hosted-step1")).toHaveCount(0);
    await expect(page.locator("input#ni-domain")).toHaveCount(0);
    // How it works, above the fold.
    await expect(page.locator(".landing-step")).toHaveCount(3);
  });

  test("I already have an ID on the launcher goes to the ID's own door, or says it doesn't exist", async ({ page, browser }) => {
    test.slow();
    // Keys for a hosted name live on its own origin, so the launcher asks no
    // passkey: it checks the name and hands over to `<name>.poweur.net`.
    // The name is claimed through 127.0.0.1 (a secure origin) in its own browser.
    const handle = `door${Date.now().toString(36)}`;
    const owner = await browser.newContext({ viewport: MOBILE });
    const ownerPage = await owner.newPage();
    await stubPasskeys(ownerPage);
    await registerIdentity(ownerPage, relay, handle);
    await owner.close();

    await stubPasskeys(page);
    await page.goto(at("id.poweur.net"));
    await page.waitForSelector("#claim-card");
    await expect(page.locator("#opt-have-id")).toHaveCount(0);
    await expect(page.locator("#signin-id-input")).toHaveCount(0);

    const missing = `nobody${Date.now().toString(36)}`;
    await page.fill("#have-id-input", missing);
    await page.click("#btn-have-id");
    await expect(page.locator("#have-id-error"))
      .toHaveText(`${missing}.poweur.net doesn't exist yet. You can claim it above.`, { timeout: 20_000 });

    await page.fill("#have-id-input", handle);
    await page.press("#have-id-input", "Enter");
    await expect(page).toHaveURL(at(`${handle}.poweur.net`), { timeout: 20_000 });
    await expect(page.locator("#btn-door-signin")).toBeVisible({ timeout: 20_000 });
  });

  test("self-hosting is a link to the guide, not a DNS form", async ({ page }) => {
    await stubPasskeys(page);
    await page.goto(at("id.poweur.net"));
    await page.waitForSelector("#claim-card");
    await expect(page.locator("#opt-own-domain")).toHaveAttribute("href", "https://poweur.org/docs/relay/self-hosting");
    await expect(page.locator("#ni-provider")).toHaveCount(0);
  });

  test("the apex is a launcher too, and is not a service banner", async ({ page }) => {
    // Before E15-T7 `GET /` here answered {"service":"poweur-relay"} to a human.
    // Navigate in the page so Chromium's host-resolver-rules apply; Playwright's
    // `page.request` does not use them and would hit the real apex over DNS.
    await stubPasskeys(page);
    await page.goto(at("poweur.net", "/"));
    await expect(page).toHaveURL(at("poweur.net"));
    await expect(page.locator("#claim-card")).toBeVisible({ timeout: 20_000 });
  });

  test("a name is judged before any passkey is spent", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    await page.goto(at("id.poweur.net"));
    await page.waitForSelector("#claim-card");

    // Reserved — the relay's own vocabulary, not a guess made in the browser.
    await page.fill("#ni-handle", "admin");
    await expect(page.locator("#ni-availability"))
      .toHaveText("This name is reserved by the operator.", { timeout: 20_000 });
    await expect(page.locator("#btn-claim")).toBeDisabled();

    // Non-ASCII: the homoglyph gate, reported as a character-set problem.
    await page.fill("#ni-handle", "аdmin");
    await expect(page.locator("#ni-availability"))
      .toHaveText("Use only a-z, 0-9 and hyphen.", { timeout: 20_000 });

    // A pasted FQDN means the same handle as the bare label (E15-T12).
    await page.fill("#ni-handle", "Melissa.poweur.net");
    await expect(page.locator("#ni-handle")).toHaveValue("melissa");
    await expect(page.locator("#ni-availability"))
      .toContainText("is available", { timeout: 20_000 });
    // This host is `http://id.poweur.net`, not a secure context, so there is
    // no PRF-capable passkey here. The name is free; creating it is not.
    await expect(page.locator("#claim-prf-required")).toContainText("does not support passkeys with PRF");
    await expect(page.locator("#btn-claim")).toBeDisabled();
  });

  test("an unclaimed identity host offers its own name and nothing else", async ({ page }) => {
    await stubPasskeys(page);
    await page.goto(at("freename.poweur.net"));

    await expect(page.locator(".door-name")).toHaveText("freename", { timeout: 20_000 });
    await expect(page.locator("#btn-door-claim")).toHaveText("Claim freename.poweur.net");
    // E15-T9: nowhere here takes a typed identity, and no other name can be made.
    await expect(page.locator("#ni-handle")).toHaveCount(0);
    await expect(page.locator("#opt-create-new")).toHaveCount(0);
  });

  test("a reserved identity host offers no claim at all", async ({ page }) => {
    await stubPasskeys(page);
    // `admin` has no identity document, so a 404-based check would call this
    // free and walk the user into a registration that refuses (E15-T9).
    await page.goto(at("admin.poweur.net"));

    await expect(page.locator(".door-message"))
      .toHaveText("This name is reserved by the operator.", { timeout: 20_000 });
    await expect(page.locator("#btn-door-claim")).toHaveCount(0);
  });

  test("a front door gets the whole window on a desktop, not the nav column", async ({ browser }) => {
    // The app shell is a grid at >=768px — nav rail, header, content. A front
    // door replaces #app's contents entirely, so an unscoped grid laid the
    // *whole landing page* into the nav column: 240px hard against the left
    // edge, with the claim button's own label clipped.
    const wide = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await wide.newPage();
    await stubPasskeys(page);
    await page.goto(at("id.poweur.net"));
    await page.waitForSelector("#claim-card");

    const box = await page.locator(".landing-body").boundingBox();
    expect(box.width).toBeGreaterThan(400);
    // Centred: the gap either side is the same, which a nav column is not.
    expect(Math.abs(box.x - (1440 - box.x - box.width))).toBeLessThan(4);
    // And nothing overflows its container.
    expect(await page.evaluate(() =>
      document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await wide.close();
  });

  test("a claimed identity host offers sign-in only", async ({ page, browser }) => {
    test.slow();
    // Claim through the same 127.0.0.1 path every other spec uses (a secure
    // origin). What this asserts is the *identity host* door for a name that
    // already exists, not the launcher ceremony — that is the rest of this file.
    await stubPasskeys(page);
    const handle = `mode${Date.now().toString(36)}`;
    await registerIdentity(page, relay, handle);

    // A *fresh* browser on that host holds nothing, so this is the door a
    // stranger — or the owner on a new device — actually sees.
    const other = await browser.newContext({ viewport: MOBILE });
    const visitor = await other.newPage();
    await stubPasskeys(visitor);
    await visitor.goto(at(`${handle}.poweur.net`));

    await expect(visitor.locator("#btn-door-signin")).toBeVisible({ timeout: 30_000 });
    await expect(visitor.locator("#btn-door-claim")).toHaveCount(0);
    await expect(visitor.locator("#ni-handle")).toHaveCount(0);
    await expect(visitor.locator("#signin-id-input")).toHaveCount(0);

    // Add this device starts the ceremony; it does not ask who you are.
    await visitor.click("#opt-join-device");
    await expect(visitor.locator("#join-identity")).toHaveCount(0);
    await expect(visitor.locator("#btn-join-start")).toHaveCount(0);
    await expect(visitor.locator(".rendezvous-code")).toBeVisible({ timeout: 30_000 });
    await expect(visitor.locator("#panel-root")).toContainText(`${handle}.poweur.net`);
    await other.close();
  });
});
