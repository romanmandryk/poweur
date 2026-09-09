/**
 * `IdentityApi.root()` against a real relay (EPIC-015 E15-T7).
 *
 * The web app resolves its front door from this document before an identity
 * exists, so the fields it reads have to be the ones the relay actually sends.
 */
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { IdentityApi } from "../src/index.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

describe("relay root document", () => {
  let relay: RunningRelay;

  beforeAll(async () => {
    relay = await startRelay({ hostedDomains: "poweur.net,example.org" });
  }, 120_000);

  afterAll(() => relay?.stop());

  it("advertises the launcher hosts a client resolves its mode against", async () => {
    const root = await new IdentityApi(relay.baseUrl).root();

    expect(root.service).toBe("poweur-relay");
    expect(root.hosted_domains).toEqual(["poweur.net", "example.org"]);
    // The default expands to `id.<d>` and the bare apex for every hosted
    // domain; the canonical one stays first.
    expect(root.launcher_hosts).toEqual([
      "id.poweur.net",
      "poweur.net",
      "id.example.org",
      "example.org",
    ]);
    expect(root.launcher_host).toBe("id.poweur.net");
  });

  it("still answers relayAddress(), which reads the same document", async () => {
    await expect(new IdentityApi(relay.baseUrl).relayAddress()).resolves.toBe(relay.address);
  });
});
