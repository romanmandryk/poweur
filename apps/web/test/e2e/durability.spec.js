import { test, expect } from "@playwright/test";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * Message durability on storage v2 (EPIC-020 Phase 9 restore list): the relay
 * inbox is a spool, so after a reload the encrypted history on the drive is
 * the only place a conversation still exists. Received and sent messages and
 * read state come back from it, and the relay stores only ciphertext.
 */
const MOBILE = { width: 375, height: 812 };

async function sendFrom(page, to, bodies) {
  await page.evaluate(async ({ to, bodies }) => {
    const { clientFor } = await window.__poweurModule("client");
    const { getActiveIdentity } = await window.__poweurModule("storage");
    const client = clientFor(getActiveIdentity());
    for (const body of bodies) await client.sendAndArchive(to, body);
  }, { to, bodies });
}

function filesUnder(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? filesUnder(path) : [path];
  });
}

test.describe("message durability", () => {
  let relay;
  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());
  test.use({ viewport: MOBILE });

  test("received and sent messages and read state survive a reload", async ({ browser }) => {
    test.slow();
    const alice = await (await browser.newContext({ viewport: MOBILE })).newPage();
    const bob = await (await browser.newContext({ viewport: MOBILE })).newPage();
    await stubPasskeys(alice);
    await stubPasskeys(bob);
    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alice, relay, `dua${suffix}`);
    const bobId = await registerIdentity(bob, relay, `dub${suffix}`);

    await sendFrom(bob, aliceId, ["durable one", "durable two", "durable three"]);
    const row = alice.locator(`.conv-row[data-compose-to="${bobId}"]`);
    await expect.poll(async () => {
      await alice.click('.nav-tab[data-page="messages"]');
      return row.count();
    }, { timeout: 40_000 }).toBeGreaterThan(0);
    await row.click();
    await expect(alice.locator(".thread-body")).toContainText("durable three");
    await alice.fill("#thread-input", "my durable reply");
    await alice.click("#btn-thread-send");
    await expect(alice.locator(".thread-body")).toContainText("my durable reply");
    // Opening the conversation read it; let the read marks reach the drive.
    await alice.click("#btn-back");
    await expect.poll(() => alice.evaluate(async (peer) => {
      const { clientFor } = await window.__poweurModule("client");
      const { getActiveIdentity } = await window.__poweurModule("storage");
      const state = await (await clientFor(getActiveIdentity()).history()).readState();
      return Boolean(state.conversations[peer]);
    }, bobId), { timeout: 20_000 }).toBe(true);

    // Keys are memory-only, so a reload is a lock; the spool was drained.
    // Without this device's snapshot, only the drive's archive can bring the
    // conversation back.
    await alice.evaluate(() => new Promise((resolve) => { const r = indexedDB.deleteDatabase("poweur-snapshots"); r.onsuccess = r.onerror = r.onblocked = () => resolve(null); }));
    await alice.reload();
    await alice.click("#btn-unlock-main");
    await alice.locator("#btn-do-unlock").click({ timeout: 30_000 });
    await expect(alice.locator(".dest-title")).toHaveText("Messages", { timeout: 45_000 });

    await expect(row).toHaveCount(1, { timeout: 30_000 });
    await expect(row.locator(".conv-preview")).toHaveText("my durable reply");
    // Read before the reload, read after it.
    await expect(alice.locator('.nav-tab[data-page="messages"] .nav-badge')).toHaveCount(0);
    await row.click();
    for (const text of ["durable one", "durable two", "durable three", "my durable reply"]) {
      await expect(alice.locator(".thread-body")).toContainText(text);
    }

    // The relay's store holds the archive only as ciphertext.
    for (const path of filesUnder(relay.dataDir)) {
      const content = readFileSync(path).toString("latin1");
      for (const text of ["durable one", "durable three", "my durable reply"]) {
        expect(content.includes(text), `${text} in ${path}`).toBe(false);
      }
    }
  });
});
