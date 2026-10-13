import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { BRIDGE_ID, OPERATOR_TOKEN, startEmailBridge } from "../helpers/email-bridge.mjs";

/**
 * Email (EPIC-023 E23-T6): opt in, receive through the real bridge, read it
 * rendered inert, reply, and see the reply leave as the user's address.
 */
const MOBILE = { width: 375, height: 812 };

test.describe("email through the bridge", () => {
  let relay;
  let bridge;

  test.beforeAll(async () => {
    test.setTimeout(240_000);
    relay = await startRelay({ env: { EMAIL_BRIDGE_ID: BRIDGE_ID, OPERATOR_TOKEN } });
    bridge = await startEmailBridge(relay);
  });
  test.afterAll(() => {
    bridge?.stop();
    relay?.stop();
  });
  test.use({ viewport: MOBILE });

  test("opt in, receive, read safely, reply", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    const handle = `mail${Date.now().toString(36)}`;
    const id = await registerIdentity(page, relay, handle);
    const address = `${handle}@poweur.net`;

    // Opt in from the page's own client (the e2e browser cannot reach
    // identity hosts to discover the bridge, so it is named here).
    const enabled = await page.evaluate(async (bridgeId) => (await window.__poweurModule("email")).enableEmail(bridgeId), BRIDGE_ID);
    expect(enabled).toBe(true);
    await expect(page.locator('.tray-tab[data-tray="email"]')).toBeVisible();

    const reply = await bridge.deliver(address, [
      "Authentication-Results: mail.poweur.net; dkim=pass header.d=example.com; spf=pass; dmarc=pass header.from=example.com",
      "From: Ann <ann@example.com>",
      `To: ${address}`,
      "Subject: Lunch on Friday?",
      "Message-ID: <lunch@example.com>",
      "MIME-Version: 1.0",
      "Content-Type: text/html; charset=utf-8",
      "",
      `<p onclick="alert(1)">Shall we <b>try</b> it?</p><script>window.parent.document.title="pwned"</script><img src="https://tracker.example/p.gif">`,
    ].join("\n"));
    expect(reply).toMatch(/^250 /);

    // The bridge's message arrives over the event stream.
    await page.click('.nav-tab[data-page="messages"]');
    await page.locator('.tray-tab[data-tray="email"]').click();
    await expect(page.locator(".mail-row")).toHaveCount(1, { timeout: 40_000 });
    await expect(page.locator(".mail-row .mail-subject")).toHaveText("Lunch on Friday?");
    await page.locator(".mail-row").first().click();

    const frame = page.locator("iframe.email-frame");
    await expect(frame).toBeVisible();
    expect(await frame.getAttribute("sandbox")).not.toContain("allow-scripts");
    await expect(page.frameLocator("iframe.email-frame").locator("b")).toHaveText("try");
    expect(await page.title()).not.toBe("pwned");
    await expect(page.locator("#btn-email-load-images")).toBeVisible();

    await page.click("#btn-email-reply");
    await expect(page.locator("#email-to")).toHaveValue("Ann <ann@example.com>");
    await expect(page.locator("#email-subject")).toHaveValue("Re: Lunch on Friday?");
    await page.locator("#email-text").fill("Yes, at noon!");
    await page.click("#btn-email-send");

    await expect.poll(() => bridge.sent().length, { timeout: 30_000 }).toBeGreaterThan(0);
    const eml = bridge.sent()[0];
    expect(eml).toContain(`<${address}>`);
    expect(eml).toContain("In-Reply-To: <lunch@example.com>");
    expect(eml).toContain("Yes, at noon!");
    expect(id).toBe(`${handle}.poweur.net`);
  });
});
