import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { acceptOffer, dropMount, validateShareOffer, type Mounts, type ShareOffer } from "../src/drive/offer.js";
import type { Share } from "../src/drive/share.js";

const vectors = JSON.parse(readFileSync(new URL("../../identity/testdata/vectors/drive-shares.json", import.meta.url), "utf8")) as { shares: Array<{ share: Share }> };
const offer = (): ShareOffer => ({ format: 2, share: vectors.shares[0]!.share, relay: "relay.poweur.net", name: "Plans", kind: "folder", offered_at: "2026-09-29T08:00:00Z" });

describe("share offers match Go's rules", () => {
  it("validates offers", () => {
    expect(() => validateShareOffer(offer())).not.toThrow();
    for (const change of [{ format: 1 }, { relay: "evil.example/redirect" }, { kind: "board" as never }, { name: "a/b" }, { offered_at: "yesterday" },
      { share: { ...vectors.shares[1]!.share } }]) {
      expect(() => validateShareOffer({ ...offer(), ...change })).toThrow();
    }
  });
  it("mounts on accept and drops on revocation", () => {
    const mounts: Mounts = { format: 1, mounts: [] };
    const accept = acceptOffer(mounts, offer(), new Date("2026-09-29T09:00:00Z"));
    expect(accept).toEqual({ format: 2, drive: offer().share.drive, share_id: offer().share.id, accepted_at: "2026-09-29T09:00:00Z" });
    acceptOffer(mounts, offer());
    expect(mounts.mounts).toHaveLength(1);
    expect(dropMount(mounts, { format: 2, drive: offer().share.drive, share_id: offer().share.id, revoked_at: "2026-09-30T00:00:00Z" })).toBe(true);
    expect(mounts.mounts).toHaveLength(0);
  });
});
