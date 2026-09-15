/**
 * Where the e2e suite finds the web client, and how it reaches the app's own
 * modules.
 */
import { readFileSync } from "node:fs";

/** The relay serves the built app (`apps/web/dist`) here. */
export function appPath() {
  return "/app/";
}

/** The version and build time the app reports in Settings → About. */
export function appBuildInfo() {
  const source = readFileSync(new URL("../../src/build-info.ts", import.meta.url), "utf8");
  return {
    APP_VERSION: source.match(/APP_VERSION\s*=\s*"([^"]+)"/)?.[1],
    APP_BUILD_TIME: source.match(/APP_BUILD_TIME\s*=\s*"([^"]+)"/)?.[1],
  };
}

/**
 * Some steps reach past the UI — seed a contact, read the archive — through the
 * app's own module instances, where the unlocked keys live. Set before any app
 * script runs, the flag makes the bundle hand those instances to
 * `window.__poweurModule` (`src/shell/testSeam.ts`).
 */
export async function installAppSeam(page) {
  await page.addInitScript(() => {
    globalThis.__POWEUR_TEST_SEAM__ = true;
  });
}
