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
    const { clientFor } = await import("./js/client.js");
    const { getActiveIdentity } = await import("./js/storage.js");
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
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
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
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
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
      const { clientFor } = await import("./js/client.js");
      const { getActiveIdentity } = await import("./js/storage.js");
      const dav = await clientFor(getActiveIdentity()).dav();
      await dav.write("shared/watched/from-elsewhere.txt", "another device");
    });

    // No click, no navigation: the poll notices and the listing catches up.
    await expect(page.locator(".conv-list")).toContainText("from-elsewhere.txt", { timeout: 30_000 });
  });
});
