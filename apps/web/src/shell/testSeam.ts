/**
 * The e2e test seam (EPIC-021 T12). Some Playwright steps reach past the UI —
 * seed a contact, read the archive — through the app's *own* module instances,
 * because the unlocked keys live in them. A bundle has no module URLs to
 * import, so it hands those instances to `__poweurModule`.
 *
 * Inert unless a test set `__POWEUR_TEST_SEAM__` before the app loaded, and it
 * grants nothing a same-origin script does not already have.
 */
import * as sdk from "@poweur/client";
import * as client from "../lib/client.js";
import * as storage from "../lib/storage.js";
import * as messages from "../actions/messages";

const MODULES: Record<string, unknown> = { client, storage, sdk, messages };

export function installTestSeam() {
  const scope = globalThis as { __POWEUR_TEST_SEAM__?: boolean; __poweurModule?: (name: string) => Promise<unknown> };
  if (!scope.__POWEUR_TEST_SEAM__) return;
  scope.__poweurModule = async (name: string) => {
    if (!(name in MODULES)) throw new Error(`no test module "${name}"`);
    return MODULES[name];
  };
}
