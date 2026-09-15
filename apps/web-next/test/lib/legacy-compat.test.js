/**
 * EPIC-021 E21-T3: /app/ and /newapp/ run on one origin, so they read and
 * write the same localStorage. An identity created in either app must unlock
 * in the other, and neither may rewrite a record into a shape the other
 * cannot read.
 *
 * Deleted at cutover (E21-T14) together with apps/web.
 */
import { beforeEach, describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import * as next from "../../src/lib/storage.js";
import * as legacy from "../../../web/js/storage.js";

const HERE = dirname(fileURLToPath(import.meta.url));
const LEGACY_JS = join(HERE, "../../../web/js");
const NEXT_LIB = join(HERE, "../../src/lib");

/** Carried over verbatim; the legacy app is feature-frozen until cutover. */
const CARRIED = [
  "client", "enroll-wait", "keystore", "mode", "native", "outbox",
  "passkey", "profiles", "signin", "storage", "threads", "vault",
];

const RECORD = {
  identity: "alice.poweur.net",
  relayUrl: "https://poweur.net",
  kdf: "prf",
  credentialId: "Y3JlZA",
  wrapped: { iv: "aXY", ct: "Y3Q" },
  publicJwks: { sign: { kty: "OKP", crv: "Ed25519", x: "eA" } },
};

describe("storage is shared between /app/ and /newapp/", () => {
  beforeEach(() => localStorage.clear());

  it("an identity saved by the legacy app loads in the new one", () => {
    legacy.saveIdentityRecord(RECORD.identity, RECORD);
    legacy.setActiveIdentity(RECORD.identity);

    expect(next.loadIdentityRecord(RECORD.identity)).toEqual(legacy.loadIdentityRecord(RECORD.identity));
    expect(next.getActiveIdentity()).toBe(RECORD.identity);
    expect(next.listIdentities()).toEqual(legacy.listIdentities());
  });

  it("an identity saved by the new app loads in the legacy one", () => {
    next.saveIdentityRecord(RECORD.identity, RECORD);
    next.setActiveIdentity(RECORD.identity);

    expect(legacy.loadIdentityRecord(RECORD.identity)).toEqual(next.loadIdentityRecord(RECORD.identity));
    expect(legacy.getActiveIdentity()).toBe(RECORD.identity);
  });

  it("writes the same bytes", () => {
    legacy.saveIdentityRecord(RECORD.identity, RECORD);
    legacy.saveConfig({ ...legacy.getConfig(), relayUrl: "https://poweur.net" });
    const written = { ...localStorage };
    localStorage.clear();

    next.saveIdentityRecord(RECORD.identity, RECORD);
    next.saveConfig({ ...next.getConfig(), relayUrl: "https://poweur.net" });
    expect({ ...localStorage }).toEqual(written);
  });

  it("removing an identity in one app removes it for the other", () => {
    next.saveIdentityRecord(RECORD.identity, RECORD);
    legacy.removeIdentity(RECORD.identity);
    expect(next.loadIdentityRecord(RECORD.identity)).toBeFalsy();
  });
});

describe("carried-over modules match the frozen legacy app", () => {
  // A fix to apps/web/js during the rewrite must land here too (EPIC-021 →
  // Goal). When a module is deliberately changed, drop it from CARRIED.
  it.each(CARRIED)("%s.js is identical", (name) => {
    const a = readFileSync(join(LEGACY_JS, `${name}.js`), "utf8");
    const b = readFileSync(join(NEXT_LIB, `${name}.js`), "utf8");
    expect(b === a, `${name}.js differs from apps/web/js/${name}.js`).toBe(true);
  });
});
