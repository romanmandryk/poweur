import { chromium } from "@playwright/test";
import { startRelay } from "./test/helpers/relay.mjs";

const relay = await startRelay();
console.log("relay", relay.baseUrl);
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
page.on("console", (m) => console.log("[console]", m.type(), m.text().slice(0, 200)));
page.on("pageerror", (e) => console.log("[pageerror]", e.message.slice(0, 300)));

await page.addInitScript(() => {
  window.__renders = 0;
  const orig = Document.prototype.getElementById;
  // count renders by hooking innerHTML writes on #app
  new MutationObserver(() => { window.__renders++; }).observe(document.documentElement, { childList: true, subtree: true });
});
await page.goto(`${relay.baseUrl}/app/`);
await page.waitForTimeout(1500);
console.log("step0 renders", await page.evaluate(() => window.__renders));
console.log("text", (await page.evaluate(() => document.body.innerText)).slice(0, 200));

await page.evaluate(() => { window.__renders = 0; });
await page.waitForTimeout(1500);
console.log("IDLE renders in 1.5s:", await page.evaluate(() => window.__renders));

await browser.close();
relay.stop();
process.exit(0);
