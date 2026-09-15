import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * E15-T4 acceptance: sharing a folder, reading and writing it as the grantee,
 * and revocation taking effect.
 *
 * Two contexts, because the whole point of a grant is what the *other* party
 * can do with it — and because revocation is only meaningful if the grantee
 * still has a live token when it happens, which is exactly this shape.
 */
const MOBILE = { width: 375, height: 812 };

/** Put a folder and a file under /shared, the way the owner's tree starts. */
async function seedSharedFolder(page, folder) {
  return page.evaluate(async ({ folder }) => {
    const { clientFor } = await window.__poweurModule("client");
    const { getActiveIdentity } = await window.__poweurModule("storage");
    const dav = await clientFor(getActiveIdentity()).dav();
    await dav.mkdir(`shared/${folder}`);
    await dav.write(`shared/${folder}/notes.txt`, "owner wrote this");
  }, { folder });
}

test.describe("files sharing", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;

  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());

  test.use({ viewport: MOBILE });

  test("owner shares a folder, grantee reads and writes it, revoke stops access", async ({ browser }) => {
    test.slow();
    const ownerCtx = await browser.newContext({ viewport: MOBILE });
    const granteeCtx = await browser.newContext({ viewport: MOBILE });
    const owner = await ownerCtx.newPage();
    const grantee = await granteeCtx.newPage();
    await stubPasskeys(owner);
    await stubPasskeys(grantee);

    const suffix = Date.now().toString(36);
    const ownerId = await registerIdentity(owner, relay, `sho${suffix}`);
    const granteeId = await registerIdentity(grantee, relay, `shg${suffix}`);

    await seedSharedFolder(owner, "project-x");

    // ── The owner shares it from the file row ──────────────────────────────
    await owner.click('.nav-tab[data-page="files"]');
    await owner.click('[data-open-dir="shared"]');
    await expect(owner.locator('[data-share="shared/project-x"]')).toBeVisible({ timeout: 20_000 });
    await owner.click('[data-share="shared/project-x"]');

    // The audience picker is the same component compose autocompletes with.
    await owner.fill(".audience-picker .idin input", granteeId);
    await owner.press(".audience-picker .idin input", "Enter");
    // Chips show the handle; the grant carries the FQDN, asserted below.
    await expect(owner.locator(".audience-chips"))
      .toContainText(granteeId.split(".")[0], { timeout: 20_000 });
    await owner.click('#share-perms [data-perm="rw"]');
    await owner.click("#btn-share-go");

    // The row says so, and the grant is listed where it can be taken back.
    await expect(owner.locator('.conv-row:has-text("project-x") .chip-accent'))
      .toHaveText("Shared", { timeout: 20_000 });
    await owner.click('[data-nav-path=""]');
    await owner.click("#btn-shares");
    await expect(owner.locator(".share-path")).toHaveText("/shared/project-x");
    await expect(owner.locator(".share-row")).toContainText("read + write");
    await expect(owner.locator(".share-row")).toContainText(granteeId);
    await owner.click("#panel-close-btn");

    // ── The grantee opens the owner's tree and reads it ────────────────────
    await grantee.click('.nav-tab[data-page="files"]');
    await grantee.click("#btn-files-shared");
    await grantee.fill(".owner-picker .idin input", ownerId);
    await grantee.press(".owner-picker .idin input", "Enter");
    await expect(grantee.locator(".visitor-banner")).toContainText(ownerId, { timeout: 20_000 });

    await grantee.click('[data-open-dir="shared"]');
    await grantee.click('[data-open-dir="shared/project-x"]');
    await expect(grantee.locator(".conv-name")).toContainText("notes.txt", { timeout: 20_000 });

    // …and writes, because the grant said read and write.
    const wrote = await grantee.evaluate(async ({ owner, path }) => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const dav = await clientFor(getActiveIdentity()).dav({ audience: owner, scope: "dav:full" });
      await dav.write(path, "grantee was here");
      return dav.readText(path);
    }, { owner: ownerId, path: "shared/project-x/reply.txt" });
    expect(wrote).toBe("grantee was here");

    // ── The owner revokes, and the next request is refused ─────────────────
    await owner.click("#btn-shares");
    await owner.click("[data-revoke]");
    await expect(owner.locator("#shares-list")).toContainText("not shared anything yet", { timeout: 20_000 });

    const refused = await grantee.evaluate(async ({ owner }) => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const dav = await clientFor(getActiveIdentity()).dav({ audience: owner, scope: "dav:full", force: true });
      try {
        await dav.list("shared/project-x");
        return "allowed";
      } catch (error) {
        return `refused:${error.status ?? error.message}`;
      }
    }, { owner: ownerId });
    expect(refused).toMatch(/^refused:/);

    await ownerCtx.close();
    await granteeCtx.close();
  });

  test("the changes feed refreshes the open folder", async ({ page }) => {
    test.slow();
    await stubPasskeys(page);
    const suffix = Date.now().toString(36);
    await registerIdentity(page, relay, `shc${suffix}`);
    await seedSharedFolder(page, "watched");

    await page.click('.nav-tab[data-page="files"]');
    await page.click('[data-open-dir="shared"]');
    await page.click('[data-open-dir="shared/watched"]');
    await expect(page.locator(".conv-name")).toContainText("notes.txt", { timeout: 20_000 });

    // Written behind the UI's back, the way another device would.
    await page.evaluate(async () => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const dav = await clientFor(getActiveIdentity()).dav();
      await dav.write("shared/watched/from-elsewhere.txt", "another device");
    });

    // No click, no navigation: the poll notices and the listing catches up.
    await expect(page.locator(".conv-list")).toContainText("from-elsewhere.txt", { timeout: 30_000 });
  });

  /**
   * E05-T4 acceptance: a capability URL works in a browser with no auth at
   * all, and revocation kills it.
   *
   * The visitor gets a brand-new context with no storage, no identity and no
   * service worker — the closest thing to "a stranger opened the link you
   * mailed them" that a test can be. The grant itself is signed in the page
   * by the owner's own key, exactly as the share dialog will do it.
   */
  test("a public link opens with no account, and revoking it kills the page", async ({ browser }) => {
    test.slow();
    const ownerCtx = await browser.newContext({ viewport: MOBILE });
    const owner = await ownerCtx.newPage();
    await stubPasskeys(owner);

    const suffix = Date.now().toString(36);
    const ownerId = await registerIdentity(owner, relay, `shl${suffix}`);
    await seedSharedFolder(owner, "handouts");

    const { shareId, token } = await owner.evaluate(async ({ folder }) => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const client = clientFor(getActiveIdentity());
      const shares = await client.shares();
      const { grant, token } = await shares.addLink(client.signer, `shared/${folder}`, {});
      return { shareId: grant.share_id, token };
    }, { folder: "handouts" });

    expect(token).toMatch(/^[a-z2-7]{26}$/);

    // A visitor with nothing: no identity, no cookies, no local storage.
    const strangerCtx = await browser.newContext({ viewport: MOBILE });
    const stranger = await strangerCtx.newPage();
    const linkUrl = `${relay.baseUrl}/s/${ownerId}/${token}`;

    const listing = await stranger.goto(linkUrl);
    expect(listing.status()).toBe(200);
    await expect(stranger.locator("body")).toContainText("notes.txt");
    await expect(stranger.locator("body")).toContainText(ownerId);

    // The page must not carry the token onward in a Referer, or be indexed.
    expect(listing.headers()["referrer-policy"]).toBe("no-referrer");
    expect(listing.headers()["x-robots-tag"]).toContain("noindex");

    // The file itself downloads.
    const file = await stranger.request.get(`${linkUrl}/notes.txt`);
    expect(file.status()).toBe(200);
    expect(await file.text()).toBe("owner wrote this");

    // Nothing outside the shared folder is reachable through the link.
    const outside = await stranger.request.get(`${linkUrl}/../../private/diary.txt`);
    expect(outside.status()).not.toBe(200);

    // ── The owner revokes it; the next load is a dead end ─────────────────
    await owner.evaluate(async ({ shareId }) => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const shares = await clientFor(getActiveIdentity()).shares();
      await shares.revoke(shareId);
    }, { shareId });

    const afterRevoke = await stranger.goto(linkUrl);
    expect(afterRevoke.status()).toBe(404);
    // A revoked link is indistinguishable from one that never existed, so it
    // must not admit that it has "expired".
    await expect(stranger.locator("body")).not.toContainText(/expired/i);

    await strangerCtx.close();
    await ownerCtx.close();
  });

  test("a password-protected link asks before it shows anything", async ({ browser }) => {
    test.slow();
    const ownerCtx = await browser.newContext({ viewport: MOBILE });
    const owner = await ownerCtx.newPage();
    await stubPasskeys(owner);

    const suffix = Date.now().toString(36);
    const ownerId = await registerIdentity(owner, relay, `shp${suffix}`);
    await seedSharedFolder(owner, "guarded");

    const token = await owner.evaluate(async () => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const client = clientFor(getActiveIdentity());
      const shares = await client.shares();
      const { token } = await shares.addLink(client.signer, "shared/guarded", {
        password: "correct horse",
      });
      return token;
    });

    const strangerCtx = await browser.newContext({ viewport: MOBILE });
    const stranger = await strangerCtx.newPage();
    const linkUrl = `${relay.baseUrl}/s/${ownerId}/${token}`;

    const gate = await stranger.goto(linkUrl);
    expect(gate.status()).toBe(401);
    await expect(stranger.locator('input[type="password"]')).toBeVisible();
    // The gate shows the form and nothing else.
    await expect(stranger.locator("body")).not.toContainText("notes.txt");

    // A wrong password gets the form back, not the files.
    await stranger.fill('input[type="password"]', "hunter2");
    await stranger.click('button[type="submit"]');
    await expect(stranger.locator('input[type="password"]')).toBeVisible();
    await expect(stranger.locator("body")).not.toContainText("notes.txt");

    // The right one opens it, and the session sticks for the next page.
    await stranger.fill('input[type="password"]', "correct horse");
    await stranger.click('button[type="submit"]');
    await expect(stranger.locator("body")).toContainText("notes.txt", { timeout: 20_000 });

    await stranger.goto(linkUrl);
    await expect(stranger.locator("body")).toContainText("notes.txt");

    await strangerCtx.close();
    await ownerCtx.close();
  });
});
