import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";

/**
 * One identity, several devices: the relay hands each message to whichever
 * device picks it up first, and the others see it only in the history on the
 * drive. An open app follows the drive's change stream, so a message another
 * device archived shows here without a reload or pull-to-refresh.
 */
const MOBILE = { width: 375, height: 812 };

test.describe("history across devices", () => {
  let relay;
  test.beforeAll(async () => { relay = await startRelay(); });
  test.afterAll(() => relay?.stop());
  test.use({ viewport: MOBILE });

  test("a message another device picked up appears without a reload", async ({ browser }) => {
    test.slow();
    const alice = await (await browser.newContext({ viewport: MOBILE })).newPage();
    const bob = await (await browser.newContext({ viewport: MOBILE })).newPage();
    await stubPasskeys(alice);
    await stubPasskeys(bob);
    const suffix = Date.now().toString(36);
    const aliceId = await registerIdentity(alice, relay, `hsa${suffix}`);
    const bobId = await registerIdentity(bob, relay, `hsb${suffix}`);

    await alice.click('.nav-tab[data-page="messages"]');
    const row = alice.locator(`.conv-row[data-compose-to="${bobId}"]`);
    await expect(row).toHaveCount(0);

    // "The other device": its own client, so its own history cache, writing
    // the archive record it would after a pickup. This tab's inbox never sees
    // the message; only the drive does.
    await alice.evaluate(async ({ me, peer }) => {
      const { signerFor } = await window.__poweurModule("client");
      const { PoweurClient } = await window.__poweurModule("sdk");
      const { getActiveIdentity, relayUrlFor } = await window.__poweurModule("storage");
      const keys = signerFor(getActiveIdentity());
      const other = new PoweurClient({ relayUrl: relayUrlFor(me), signer: keys.signer, decryptor: keys.decryptor });
      const { archived } = await other.archive([{
        version: 1,
        id: `msg_other_device_${Date.now()}`,
        sender: peer,
        recipient: me,
        timestamp: new Date().toISOString(),
        type: "chat.text",
        queue: "inbox",
        body: "picked up on the other device",
      }]);
      if (archived !== 1) throw new Error("the other device could not archive");
    }, { me: aliceId, peer: bobId });

    // No navigation, no reload: the drive event alone brings it in.
    await expect(row).toHaveCount(1, { timeout: 15_000 });
    await row.click();
    await expect(alice.locator(".thread-body")).toContainText("picked up on the other device");
  });
});
