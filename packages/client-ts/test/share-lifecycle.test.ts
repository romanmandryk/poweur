import { describe, expect, it } from "vitest";

import { generateSigningKeypair } from "../src/crypto/index.js";
import { LocalSigner } from "../src/crypto/keys.js";
import { canonicalShareGrant } from "../src/canonical.js";
import {
  buildFileRequestGrant,
  buildGrant,
  buildShareOffer,
  defaultShareMountName,
  normalizeShareMountPath,
  validateGrant,
  validateShareClaim,
  validateShareMount,
  validateShareOffer,
} from "../src/shares.js";

describe("share lifecycle", () => {
  it("builds and verifies a direct signed offer", async () => {
    const { privateKey } = generateSigningKeypair();
    const signer = new LocalSigner("alice.poweur.net", privateKey);
    const grant = await buildGrant(signer, "shared/project-x", {
      with: ["bob.poweur.net"], permissions: "rw", shareId: "shr_lifecycle",
    });
    const offer = buildShareOffer(grant, "2026-09-23T20:00:00Z");

    expect(() => validateShareOffer(offer, "bob.poweur.net", signer.publicKey)).not.toThrow();
    expect(() => validateShareOffer(offer, "mallory.poweur.net", signer.publicKey)).toThrow(/not a direct audience/);
    expect(() => validateShareOffer({ ...offer, grant: { ...grant, signature: "AAAA" } }, "bob.poweur.net", signer.publicKey)).toThrow(/signature/);
  });

  it("normalizes mount names and rejects path confusion", () => {
    expect(defaultShareMountName("shared/.project-x.")).toBe("project-x");
    expect(normalizeShareMountPath("/shared/alice.poweur.net/project-x/", "alice.poweur.net")).toBe("shared/alice.poweur.net/project-x");
    expect(() => normalizeShareMountPath("shared/alice.poweur.net/../x", "alice.poweur.net")).toThrow();
    expect(() => normalizeShareMountPath("shared/mallory.poweur.net/x", "alice.poweur.net")).toThrow();
  });

  it("validates credential-free mount pointers", () => {
    expect(() => validateShareMount({
      version: 1,
      share_id: "shr_1",
      owner: "alice.poweur.net",
      source_path: "shared/project-x",
      permissions: ["read"],
      accepted_at: "2026-09-23T20:00:00Z",
    })).not.toThrow();
    expect(() => validateShareMount({
      version: 1,
      share_id: "shr_1",
      owner: "alice.poweur.net",
      source_path: "private/project-x",
      permissions: ["read"],
      accepted_at: "2026-09-23T20:00:00Z",
    })).toThrow();
  });

  it("builds create-only file requests with signed limits", async () => {
    const { privateKey } = generateSigningKeypair();
    const signer = new LocalSigner("alice.poweur.net", privateKey);
    const { grant } = await buildFileRequestGrant(signer, "shared/inbox", {
      shareId: "shr_request",
      maxUploads: 2,
      maxBytes: 4096,
      maxObjectBytes: 2048,
      allowedTypes: ["text/plain", "image/*"],
      notify: true,
    });
    expect(grant.permissions).toEqual(["create"]);
    expect(grant.link?.file_request?.max_uploads).toBe(2);
    expect(canonicalShareGrant(grant)).toContain(
      "\npoweur-file-request\n2\n4096\n2048\nimage/*,text/plain\ntrue",
    );
    expect(() => validateGrant(grant)).not.toThrow();
    expect(() => validateGrant({ ...grant, permissions: ["read"] })).toThrow(/exactly the create/);
    expect(() => validateGrant({
      ...grant,
      link: { ...grant.link, max_downloads: 1 },
    })).toThrow(/cannot set max_downloads/);
  });

  it("validates guest-to-ID claim continuity payloads", () => {
    const claim = {
      version: 1 as const,
      share_id: "shr_request",
      owner: "alice.poweur.net",
      token: "aaaaaaaaaaaaaaaaaaaaaaaaaa",
      claimant: "bob.poweur.net",
      action: "uploaded" as const,
      claimed_at: "2026-09-24T08:00:00Z",
    };
    expect(() => validateShareClaim(claim)).not.toThrow();
    expect(() => validateShareClaim({ ...claim, token: "bad" })).toThrow(/26 characters/);
    expect(() => validateShareClaim({ ...claim, action: "edited" as any })).toThrow(/action/);
  });
});
