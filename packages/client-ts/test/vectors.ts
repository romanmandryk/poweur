/**
 * Loader for the Go-generated conformance fixtures.
 *
 * The vectors live in `packages/identity/testdata/vectors/` and are produced
 * by the Go generators (see E17-T5 in EPIC-017). A missing directory means
 * they have not been generated in this checkout — the suite fails loudly
 * rather than silently passing with nothing to check.
 */

import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
// test → client-ts → packages → repo root
export const VECTORS_DIR = join(here, "../../identity/testdata/vectors");

export function vectorsAvailable(): boolean {
  return existsSync(VECTORS_DIR) && readdirSync(VECTORS_DIR).some((f) => f.endsWith(".json"));
}

export function loadVectors<T>(name: string): T {
  const path = join(VECTORS_DIR, `${name}.json`);
  if (!existsSync(path)) {
    throw new Error(
      `missing conformance vector ${name}.json — regenerate with ` +
        "`go test ./packages/identity/... ./apps/api/internal/crypto/... ./apps/cli/internal/crypto/...`",
    );
  }
  return JSON.parse(readFileSync(path, "utf8")) as T;
}
