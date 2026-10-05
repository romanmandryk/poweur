import { test, expect } from "@playwright/test";
import { startRelay } from "../helpers/relay.mjs";
import { registerIdentity, stubPasskeys } from "../helpers/app-ui.mjs";
import { startGuestbook } from "../helpers/guestbook.mjs";

/**
 * "Sign in with Poweur ID" end to end, in a browser: the guestbook is the relying party, the
 * relay's web app is the signer, and the visitor is a real Poweur ID created in that app.
 * This is the journey the launch demo shows, so it must not break unnoticed.
 */
test.describe("Guestbook sign-in", () => {
  let relay;
  let guestbook;

  test.beforeAll(async () => {
    relay = await startRelay();
    guestbook = await startGuestbook({ relay });
  });

  test.afterAll(() => {
    guestbook?.stop();
    relay?.stop();
  });

  test("a Poweur ID signs in, leaves a message and the page shows it", async ({ page, context }) => {
    await stubPasskeys(page);
    const identity = await registerIdentity(page, relay, "gbvisitor");

    const book = await context.newPage();
    await book.goto(guestbook.origin);
    await expect(book.locator("#signin")).toBeVisible();
    await expect(book.locator("#compose")).toBeHidden();

    await book.click("#signin");
    await expect(book.locator("#pending")).toBeVisible();
    const match = ((await book.locator("#match").textContent()) ?? "").replace(/\s+/g, "");
    expect(match).toMatch(/^\d{2,}$/);
    const requestLink = (await book.locator("#approve-url").textContent()).trim();
    expect(requestLink).toMatch(new RegExp(`^${guestbook.origin}/auth/r/`));

    // The guestbook's own link sends a browser to the visitor's signer (poweur.net's app in
    // production, the test relay's here); the signer shows what is being approved.
    const signer = await context.newPage();
    await stubPasskeys(signer);
    await signer.goto(`${relay.baseUrl}/app/?auth=${encodeURIComponent(requestLink)}`);
    await expect(signer.locator("text=Poweur Guestbook").first()).toBeVisible({ timeout: 45_000 });
    await expect(signer.locator("#btn-auth-unlock, #btn-auth-approve")).toBeVisible({ timeout: 45_000 });
    if (await signer.locator("#btn-auth-unlock").count()) await signer.click("#btn-auth-unlock");
    await expect(signer.locator("#btn-auth-approve")).toBeVisible({ timeout: 45_000 });
    // The signer asks for the code shown on the screen that started the sign-in.
    await signer.fill("#auth-match", match);
    await signer.click("#btn-auth-approve");

    // The starting page notices on its own and unlocks the message box.
    await expect(book.locator("#who")).toHaveText(identity, { timeout: 45_000 });
    await expect(book.locator("#compose")).toBeVisible();

    await book.locator("#editor").click();
    await book.keyboard.type("Hello from the browser test");
    await expect(book.locator("#send")).toBeEnabled();
    await book.click("#send");
    await expect(book.locator("#entries")).toContainText("Hello from the browser test", { timeout: 30_000 });
    await expect(book.locator("#entries")).toContainText(identity);

    // A reload keeps the session and the entry; signing out puts the sign-in button back.
    await book.reload();
    await expect(book.locator("#who")).toHaveText(identity, { timeout: 20_000 });
    await expect(book.locator("#entries")).toContainText("Hello from the browser test");
    await book.click("#logout");
    await expect(book.locator("#signin")).toBeVisible();
    await expect(book.locator("#entries")).toContainText("Hello from the browser test");
  });

  test("the approval screen can hand the request to a different ID", async ({ page, context }) => {
    await stubPasskeys(page);
    await registerIdentity(page, relay, "gbfirst");

    const book = await context.newPage();
    await book.goto(guestbook.origin);
    await book.click("#signin");
    await expect(book.locator("#pending")).toBeVisible();
    const requestLink = (await book.locator("#approve-url").textContent()).trim();

    const signer = await context.newPage();
    await stubPasskeys(signer);
    await signer.goto(`${relay.baseUrl}/app/?auth=${encodeURIComponent(requestLink)}`);
    await expect(signer.locator("text=Poweur Guestbook").first()).toBeVisible({ timeout: 45_000 });
    await expect(signer.locator("#auth-other")).toBeVisible({ timeout: 45_000 });

    // An ID that is not in this browser: its own app opens with the same request.
    const handedOver = new Promise((resolve) => {
      signer.route("**://someoneelse.poweur.net:*/**", (route) => {
        resolve(route.request().url());
        return route.fulfill({ status: 200, contentType: "text/html", body: "<title>other</title>" });
      });
    });
    await signer.locator("#auth-other summary").click();
    await signer.fill("#auth-other-input", "someoneelse.poweur.net");
    await signer.click("#btn-auth-other");
    const url = new URL(await handedOver);
    expect(url.hostname).toBe("someoneelse.poweur.net");
    expect(url.searchParams.get("auth")).toBe(requestLink);

    // Not an ID at all: nothing happens but a hint.
    const again = await context.newPage();
    await stubPasskeys(again);
    await again.goto(`${relay.baseUrl}/app/?auth=${encodeURIComponent(requestLink)}`);
    await again.locator("#auth-other summary").click();
    await again.fill("#auth-other-input", "nonsense");
    await again.click("#btn-auth-other");
    await expect(again.locator("#auth-other")).toContainText("Enter an ID");
  });

  test("a visitor who is not signed in cannot post", async ({ request }) => {
    const res = await request.post(`${guestbook.origin}/api/entries`, { data: { message: "sneaky" } });
    expect(res.status()).toBe(401);
  });
});
