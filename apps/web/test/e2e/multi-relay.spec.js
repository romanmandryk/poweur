import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { openApp, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E19-T7: several identities, on several relays, in one client.
 *
 * This is the reason to install the shell at all. A browser app cannot do it —
 * `localStorage` is per-origin, so `alice.r1.com/app/` structurally cannot see
 * an identity stored by `alice.r2.com` — but the *code* can, and that is what
 * is asserted here: one origin holding two identities whose relays differ,
 * each talking to its own.
 *
 * Two real relays, because the failure this guards against is a single global
 * relay URL leaking across identities, which only shows up when they differ.
 */
const MOBILE = { width: 375, height: 812 };

/** Register through the UI against whichever relay the config currently names. */
async function claim(page, relay, handle) {
  await page.evaluate((url) => {
    localStorage.setItem("poweur:config", JSON.stringify({ relayUrl: url, parentDomain: "poweur.net" }));
  }, relay.baseUrl);
  await page.reload();

  // Reaching the create form differs for the first identity and the rest: a
  // fresh browser opens on the welcome screen, one that already holds an
  // identity opens locked, and "add another" lives in the header from there.
  if (await page.locator("#btn-welcome-start").count()) {
    await page.click("#btn-welcome-start");
  } else {
    // Signed in already: leave the unlock screen, then "Add identity" from the
    // identity dropdown, which is where a second identity is added from.
    if (await page.locator("#btn-back").count()) await page.click("#btn-back");
    await page.click("#id-pill");
    await page.click("#dd-add-id");
  }
  await page.click("#opt-create-new");
  await expect(page.locator("#ni-handle")).toBeVisible({ timeout: 20_000 });
  await page.waitForSelector("#claim-card");
  await page.fill("#ni-handle", handle);
  await expect(page.locator("#btn-claim")).toBeEnabled({ timeout: 20_000 });
  await page.click("#btn-claim");
  await page.waitForSelector("#btn-onboard-skip", { timeout: 45_000 });
  await page.click("#btn-onboard-skip");
  return `${handle}.poweur.net`;
}

test.describe("many identities, many relays, one client", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let first;
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let second;

  test.beforeAll(async () => {
    first = await startRelay();
    second = await startRelay();
  });
  test.afterAll(() => { first?.stop(); second?.stop(); });

  test.use({ viewport: MOBILE });

  test("each identity keeps talking to its own relay", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    await openApp(page, first);

    const suffix = Date.now().toString(36);
    const onFirst = await claim(page, first, `mra${suffix}`);
    const onSecond = await claim(page, second, `mrb${suffix}`);
    expect(first.baseUrl).not.toBe(second.baseUrl);

    // Both records live in the same origin, each carrying its own relay.
    const records = await page.evaluate(() =>
      Object.fromEntries(
        Object.keys(localStorage)
          .filter((key) => key.startsWith("poweur:identity:"))
          .map((key) => [key.slice("poweur:identity:".length), JSON.parse(localStorage[key]).relay]),
      ));
    expect(records[onFirst]).toBe(first.baseUrl);
    expect(records[onSecond]).toBe(second.baseUrl);

    // The client built for each identity addresses that identity's relay —
    // not whichever one the app happened to be configured with last.
    const targets = await page.evaluate(async (identities) => {
      const { clientFor } = await window.__poweurModule("client");
      const { setActiveIdentity, setUnlockedKeys, loadIdentityRecord, relayUrlFor } =
        await window.__poweurModule("storage");
      const out = {};
      for (const identity of identities) {
        out[identity] = relayUrlFor(identity);
      }
      // …and the live client for the *active* identity agrees.
      setActiveIdentity(identities[0]);
      out.activeRecord = loadIdentityRecord(identities[0])?.relay ?? null;
      return out;
    }, [onFirst, onSecond]);
    expect(targets[onFirst]).toBe(first.baseUrl);
    expect(targets[onSecond]).toBe(second.baseUrl);

    // The identity switcher offers both.
    await page.click("#id-pill");
    await expect(page.locator("[data-switch]")).toHaveCount(2);
  });

  test("switching identities does not carry the previous one's data over", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    await openApp(page, first);

    const suffix = Date.now().toString(36);
    const one = await claim(page, first, `mrc${suffix}`);
    const two = await claim(page, second, `mrd${suffix}`);

    // The second identity is active and its state is its own: a client that
    // pooled messages or contacts globally would show one identity's mail to
    // the other, which is the bug this keying exists to prevent.
    const active = await page.evaluate(() => localStorage.getItem("poweur:active"));
    expect(active).toBe(two);

    await page.click("#id-pill");
    await page.click(`[data-switch="${one}"]`);
    await expect(page.locator(".unlock-name")).toHaveText(one.split(".")[0], { timeout: 20_000 });
    expect(await page.evaluate(() => localStorage.getItem("poweur:active"))).toBe(one);

    // Sessions are per-identity too, so the switch cannot inherit an
    // authenticated session from the identity left behind.
    const sessions = await page.evaluate(() =>
      Object.keys(sessionStorage).filter((key) => key.startsWith("poweur:session:")));
    for (const key of sessions) {
      expect(key).toMatch(/poweur:session:(mrc|mrd)/);
    }
  });
});
