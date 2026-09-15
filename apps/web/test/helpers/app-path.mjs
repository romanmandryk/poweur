/**
 * Which web client the e2e suite drives (EPIC-021 T12).
 *
 * One suite, two apps: `/app/` is the legacy client, `/newapp/` the React
 * rewrite. `POWEUR_APP_PATH=/newapp/ pnpm test:e2e` (or `pnpm web-next:test:e2e`
 * from the repo root) runs every spec against the rewrite.
 */
import { readFileSync } from "node:fs";

export function appPath() {
  const path = process.env.POWEUR_APP_PATH || "/app/";
  return path.endsWith("/") ? path : `${path}/`;
}

export const isNextApp = () => appPath() === "/newapp/";

/** The version and build time the app under test reports in Settings → About. */
export function appBuildInfo() {
  const file = isNextApp()
    ? new URL("../../../web-next/src/build-info.ts", import.meta.url)
    : new URL("../../js/build-info.js", import.meta.url);
  const source = readFileSync(file, "utf8");
  return {
    APP_VERSION: source.match(/APP_VERSION\s*=\s*"([^"]+)"/)?.[1],
    APP_BUILD_TIME: source.match(/APP_BUILD_TIME\s*=\s*"([^"]+)"/)?.[1],
  };
}

/**
 * Give page scripts one way to reach the app's own modules. Installed before
 * any app script runs; the legacy app answers from its served tree, the
 * rewrite replaces this loader with its bundled modules at boot.
 */
export async function installAppSeam(page) {
  await page.addInitScript(() => {
    globalThis.__POWEUR_TEST_SEAM__ = true;
    globalThis.__poweurModule = async (name) =>
      name === "sdk" ? import("@poweur/client") : import(new URL(`js/${name}.js`, document.baseURI).href);
  });
}
