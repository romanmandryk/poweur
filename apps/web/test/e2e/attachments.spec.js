import { readFile } from "node:fs/promises";

import { test, expect } from "@playwright/test";

import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

const MOBILE = { width: 375, height: 812 };

test("a web attachment is downloaded and hash-verified by its recipient", async ({ browser }) => {
  const relay = await startRelay();
  const aliceCtx = await browser.newContext({ viewport: MOBILE });
  const bobCtx = await browser.newContext({ viewport: MOBILE });
  const alice = await aliceCtx.newPage();
  const bob = await bobCtx.newPage();
  try {
    await stubPasskeys(alice);
    await stubPasskeys(bob);
    const suffix = Date.now().toString(36);
    await registerIdentity(alice, relay, `atta${suffix}`);
    const bobId = await registerIdentity(bob, relay, `attb${suffix}`);

    await alice.click("#btn-compose");
    const compose = alice.locator(".compose-body");
    await compose.locator(".idin input").fill(bobId);
    await expect(compose.locator(".idin-status")).toContainText("Found", { timeout: 20_000 });
    await compose.locator("#c-body").fill("browser attachment");
    const contents = Buffer.from("the attachment bytes\n");
    await compose.locator("#c-attachment").setInputFiles({
      name: "evidence.txt", mimeType: "text/plain", buffer: contents,
    });
    await alice.click("#btn-send-msg");
    await expect(alice.locator(".compose-status")).toContainText("Sent", { timeout: 30_000 });

    const open = bob.locator('[data-download-attachment]').filter({ hasText: "Open" });
    await expect(open).toBeVisible({ timeout: 30_000 });
    const downloadPromise = bob.waitForEvent("download");
    await open.click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe("evidence.txt");
    expect(await readFile(await download.path())).toEqual(contents);
  } finally {
    await aliceCtx.close();
    await bobCtx.close();
    relay.stop();
  }
});
