/**
 * Files, sync and shares against a real relay — the `poweur dav` / `sync` /
 * `share` surface, driven from TypeScript.
 */

import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { DavClient, ROOT_INFO, formatBytes, parseMultistatus } from "../src/files.js";
import { RelayError } from "../src/errors.js";
import { SyncClient } from "../src/sync.js";
import { normalizeGrantPath, verifyGrantSignature } from "../src/shares.js";
import { PROFILE_PATH } from "../src/profile.js";
import { createTestIdentity, type TestIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

describe("TypeScript client ↔ real relay (files, sync, shares)", () => {
  let relay: RunningRelay;
  let owner: TestIdentity;
  let visitor: TestIdentity;
  let dav: DavClient;

  beforeAll(async () => {
    relay = await startRelay();
    owner = await createTestIdentity(relay.baseUrl, "owner");
    visitor = await createTestIdentity(relay.baseUrl, "visitor");
    dav = await owner.client.dav();
  }, 180_000);

  afterAll(() => relay?.stop());

  it("mints a dav:full token for our own tree", async () => {
    const token = await owner.client.davToken();
    expect(token.token).toBeTruthy();
    expect(token.scope).toBe("dav:full");
    expect(token.audience).toBe(owner.identity);
  });

  it("defaults to dav:read for someone else's tree", async () => {
    const token = await owner.client.davToken({ audience: visitor.identity });
    expect(token.scope).toBe("dav:read");
  });

  it("lists the standard roots from storage-model.md", async () => {
    const entries = await dav.list("");
    const names = entries.map((entry) => entry.name);
    for (const root of Object.keys(ROOT_INFO)) expect(names).toContain(root);
    expect(entries.every((entry) => entry.dir)).toBe(true);
  });

  it("runs the full file lifecycle: mkdir, write, list, read, move, delete", async () => {
    await dav.mkdir("private/docs");
    await dav.write("private/docs/note.txt", "hello dav");

    let entries = await dav.list("private/docs");
    const note = entries.find((entry) => entry.name === "note.txt");
    expect(note?.dir).toBe(false);
    expect(note?.size).toBe("hello dav".length);
    expect(note?.etag).toBeTruthy();

    expect(await dav.readText("private/docs/note.txt")).toBe("hello dav");

    await dav.move("private/docs/note.txt", "private/docs/renamed.txt");
    entries = await dav.list("private/docs");
    expect(entries.some((entry) => entry.name === "renamed.txt")).toBe(true);
    expect(entries.some((entry) => entry.name === "note.txt")).toBe(false);

    expect(await dav.remove("private/docs")).toBe(true);
    entries = await dav.list("private");
    expect(entries.some((entry) => entry.name === "docs")).toBe(false);
  });

  it("returns null for a missing file rather than throwing", async () => {
    expect(await dav.readOptional("private/definitely-absent.json")).toBeNull();
  });

  it("throws a typed error when reading a missing file directly", async () => {
    await expect(dav.readText("private/definitely-absent.json")).rejects.toBeInstanceOf(RelayError);
  });

  it("reports quota that reflects uploaded bytes", async () => {
    await dav.write("private/quota-probe.bin", "x".repeat(1024));
    const quota = await dav.quota();
    expect(quota.used_bytes).toBeGreaterThanOrEqual(1024);
    expect(formatBytes(quota.used_bytes)).toMatch(/B|KB|MB/);
  });

  it("streams a manifest with a cursor and a changes feed", async () => {
    const sync = new SyncClient(owner.client.relay, dav.identity, dav.token);
    await dav.write("public/manifest-probe.txt", "one");

    const { entries, cursor } = await sync.manifest();
    expect(cursor).toBeTruthy();
    expect(entries.some((entry) => entry.path === "public/manifest-probe.txt")).toBe(true);

    await dav.write("public/manifest-probe-2.txt", "two");
    const { changes, fullResync } = await sync.changes(cursor);
    expect(fullResync).toBe(false);
    expect(changes.some((change) => change.path === "public/manifest-probe-2.txt")).toBe(true);
  });

  it("uploads through the resumable endpoint when the file is large enough", async () => {
    const sync = new SyncClient(owner.client.relay, dav.identity, dav.token, {
      // Force the chunked path on a small file rather than uploading 64 MiB.
      chunkThreshold: 1024,
      chunkSize: 512,
    });
    const body = new TextEncoder().encode("z".repeat(4096));
    await sync.upload("private/chunked.bin", body);
    expect(await dav.readText("private/chunked.bin")).toHaveLength(4096);
  });

  it("creates a share the relay accepts and the audience can read", async () => {
    await dav.mkdir("shared/project-x");
    await dav.write("shared/project-x/plan.md", "# the plan");

    const shares = await owner.client.shares();
    const grant = await shares.add(owner.client.signer, "shared/project-x", {
      with: [visitor.identity],
    });
    expect(grant.share_id).toMatch(/^shr_[0-9a-f]{16}$/);
    expect(verifyGrantSignature(grant, owner.client.signer.publicKey)).toBe(true);

    const listed = await shares.list();
    expect(listed.map((g) => g.share_id)).toContain(grant.share_id);

    // The visitor mints a token scoped to the owner's tree and reads through it.
    const visitorDav = await visitor.client.dav({ audience: owner.identity });
    expect(await visitorDav.readText("shared/project-x/plan.md")).toBe("# the plan");

    expect(await shares.revoke(grant.share_id)).toBe(true);
    const afterRevoke = await visitor.client.dav({ audience: owner.identity, force: true });
    await expect(afterRevoke.readText("shared/project-x/plan.md")).rejects.toBeTruthy();
  });

  it("manages groups and shares to them", async () => {
    const shares = await owner.client.shares();
    const group = await shares.setGroup(owner.client.signer, "team", [visitor.identity]);
    expect(group.members).toEqual([visitor.identity]);
    expect((await shares.listGroups()).some((g) => g.group === "team")).toBe(true);

    const grant = await shares.add(owner.client.signer, "shared/project-x", {
      withGroups: ["team"],
      permissions: "rw",
    });
    expect(grant.permissions).toEqual(["read", "write"]);

    expect(await shares.removeGroup("team")).toBe(true);
    await shares.revoke(grant.share_id);
  });

  it("refuses to sign a grant outside a shareable root", async () => {
    const shares = await owner.client.shares();
    await expect(
      shares.add(owner.client.signer, "private/secret", { with: [visitor.identity] }),
    ).rejects.toThrow(/may only cover paths under/);
    await expect(
      shares.add(owner.client.signer, "shared", { with: [visitor.identity] }),
    ).rejects.toThrow(/cannot grant the shared root itself/);
    expect(() => normalizeGrantPath("shared/ok")).not.toThrow();
  });

  it("refuses a grant with no audience", async () => {
    const shares = await owner.client.shares();
    await expect(shares.add(owner.client.signer, "shared/project-x", {})).rejects.toThrow(
      /at least one recipient or group/,
    );
  });

  it("writes and reads contacts through poweur-sys", async () => {
    const contacts = await owner.client.contacts();
    await contacts.set(visitor.identity, "accepted", { petname: "Vee" });
    const file = await contacts.load();
    const entry = file.contacts.find((c) => c.identity === visitor.identity.toLowerCase());
    expect(entry?.state).toBe("accepted");
    expect(entry?.petname).toBe("Vee");
    // Accepting pins the key that resolved at that moment (TOFU).
    expect(entry?.pinned_key).toBe(visitor.client.signer.publicKey);

    expect((await contacts.checkPin(visitor.identity)).status).toBe("ok");
    expect(await contacts.remove(visitor.identity)).toBe(true);
    expect((await contacts.load()).contacts).toHaveLength(0);
  });

  it("round-trips a profile, and the relay refuses an off-tree avatar", async () => {
    const written = await owner.client.setProfile({
      version: 1,
      display_name: "Owner",
      bio: "  writes tests  ",
      avatar: "public/avatar.png",
      links: [{ label: "site", url: "https://example.org" }, { url: "" }],
    });
    // Empty fields are dropped rather than written as empty strings, and the
    // empty link never reaches the document.
    expect(written.links).toHaveLength(1);
    expect(written.bio).toBe("writes tests");

    const { profile, explicit } = await owner.client.profile();
    expect(explicit).toBe(true);
    expect(profile.display_name).toBe("Owner");
    expect(profile.avatar).toBe("public/avatar.png");

    // The avatar rule is what stops a profile pointing at a third-party host;
    // the client refuses before the relay has to.
    await expect(
      owner.client.setProfile({ version: 1, avatar: "https://cdn.example.org/a.png" }),
    ).rejects.toThrow(/under public\//);

    // …and the relay refuses it too, for a client that skipped the check.
    const dav = await owner.client.dav();
    await expect(
      dav.writeJson(PROFILE_PATH, { version: 1, avatar: "https://cdn.example.org/a.png" }),
    ).rejects.toBeTruthy();
  });

  it("reports no profile for an identity that never wrote one", async () => {
    const fresh = await createTestIdentity(relay.baseUrl, "noprofile");
    const { profile, explicit } = await fresh.client.profile();
    expect(explicit).toBe(false);
    expect(profile).toEqual({ version: 1 });
  });

  it("parses a multistatus body without a DOM", () => {
    const xml = `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
      <D:response><D:href>/dav/alice.poweur.net/private/</D:href>
        <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
      </D:response>
      <D:response><D:href>/dav/alice.poweur.net/private/a%20b.txt</D:href>
        <D:propstat><D:prop><D:resourcetype/><D:getcontentlength>12</D:getcontentlength>
        <D:getetag>"abc"</D:getetag></D:prop></D:propstat>
      </D:response>
    </D:multistatus>`;
    const entries = parseMultistatus(xml, "private");
    expect(entries).toHaveLength(1);
    expect(entries[0]?.name).toBe("a b.txt");
    expect(entries[0]?.size).toBe(12);
  });

  it("formats byte sizes", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
  });

  // Device registry (EPIC-004 E04-T6). The browser is a device like any
  // other; the owner has to be able to see it and cut it off.
  describe("device registry", () => {
    it("lists the devices the relay has seen", async () => {
      const registry = await dav.devices();
      expect(registry.identity).toBe(owner.identity);
      expect(Array.isArray(registry.devices)).toBe(true);
      for (const device of registry.devices) {
        expect(device.id).toMatch(/^dev_[a-z2-7]{16}$/);
      }
    });

    it("refuses to revoke anything that is not a device id", async () => {
      for (const bad of ["", "nope", "dev_../../etc/passwd", "sess_abcdefghijklmnop"]) {
        await expect(dav.revokeDevice(bad)).rejects.toThrow();
      }
    });

    it("reports a well-formed but unknown device as not found", async () => {
      await expect(dav.revokeDevice("dev_aaaaaaaaaaaaaaaa")).rejects.toMatchObject({ status: 404 });
    });

    it("keeps one identity's registry away from another", async () => {
      const snoop = new DavClient(visitor.client.relay, owner.identity, (await visitor.client.davToken()).token);
      await expect(snoop.devices()).rejects.toMatchObject({ status: 401 });
    });
  });
});
