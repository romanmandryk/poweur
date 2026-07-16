/**
 * Web client ↔ real relay WebDAV (EPIC-003) — parity with `poweur dav` +
 * apps/integration TestINT_DAV flows: token mint, owner file lifecycle,
 * quota, and cross-identity /public visitor reads.
 */
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { createHostedIdentity } from "../js/messaging.js";
import {
  ROOT_INFO,
  mintDavToken,
  listDir,
  uploadFile,
  downloadFile,
  makeDir,
  moveEntry,
  deleteEntry,
  fetchQuota,
  parseMultistatus,
  fmtBytes,
} from "../js/files.js";
import { startRelay } from "./helpers/relay.mjs";

describe("web client ↔ real relay (WebDAV files)", () => {
  /** @type {Awaited<ReturnType<typeof startRelay>>} */
  let relay;
  let owner;
  let visitor;
  let ownerToken;

  beforeAll(async () => {
    relay = await startRelay();
    const suffix = Date.now().toString(36);
    owner = await createHostedIdentity(relay.baseUrl, `filesown${suffix}.poweur.net`);
    visitor = await createHostedIdentity(relay.baseUrl, `filesvis${suffix}.poweur.net`);
    const tok = await mintDavToken(relay.baseUrl, owner.identity, owner.signingJWK);
    expect(tok.token).toBeTruthy();
    expect(tok.scope).toBe("dav:full");
    ownerToken = tok.token;
  }, 90_000);

  afterAll(() => {
    relay?.stop();
  });

  it("root listing shows the standard roots from storage-model.md", async () => {
    const entries = await listDir(relay.baseUrl, owner.identity, ownerToken, "");
    const names = entries.map((e) => e.name);
    for (const root of Object.keys(ROOT_INFO)) {
      expect(names).toContain(root);
    }
    expect(entries.every((e) => e.dir)).toBe(true);
  });

  it("owner lifecycle: mkdir, upload, list, download, rename, delete", async () => {
    await makeDir(relay.baseUrl, owner.identity, ownerToken, "private/docs");
    await uploadFile(relay.baseUrl, owner.identity, ownerToken, "private/docs/note.txt", "hello dav");

    let entries = await listDir(relay.baseUrl, owner.identity, ownerToken, "private/docs");
    const note = entries.find((e) => e.name === "note.txt");
    expect(note).toBeTruthy();
    expect(note.dir).toBe(false);
    expect(note.size).toBe("hello dav".length);
    expect(note.etag).toBeTruthy();

    const res = await downloadFile(relay.baseUrl, owner.identity, ownerToken, "private/docs/note.txt");
    expect(await res.text()).toBe("hello dav");

    await moveEntry(relay.baseUrl, owner.identity, ownerToken, "private/docs/note.txt", "private/docs/renamed.txt");
    entries = await listDir(relay.baseUrl, owner.identity, ownerToken, "private/docs");
    expect(entries.some((e) => e.name === "renamed.txt")).toBe(true);
    expect(entries.some((e) => e.name === "note.txt")).toBe(false);

    await deleteEntry(relay.baseUrl, owner.identity, ownerToken, "private/docs");
    entries = await listDir(relay.baseUrl, owner.identity, ownerToken, "private");
    expect(entries.some((e) => e.name === "docs")).toBe(false);
  });

  it("quota reflects uploaded bytes", async () => {
    await uploadFile(relay.baseUrl, owner.identity, ownerToken, "private/quota-probe.bin", "x".repeat(1024));
    const quota = await fetchQuota(relay.baseUrl, owner.identity, ownerToken);
    expect(quota.provider).toBe("relay-fs");
    expect(quota.used_bytes).toBeGreaterThanOrEqual(1024);
    expect(quota.quota_bytes).toBeGreaterThan(0);
  });

  it("visitor can read /public but cannot read or write /private", async () => {
    await uploadFile(relay.baseUrl, owner.identity, ownerToken, "public/hello.txt", "public hi");
    await uploadFile(relay.baseUrl, owner.identity, ownerToken, "private/secret.txt", "secret");

    const vt = await mintDavToken(relay.baseUrl, visitor.identity, visitor.signingJWK, {
      audience: owner.identity,
      scope: "dav:read",
    });
    expect(vt.scope).toBe("dav:read");

    const pub = await downloadFile(relay.baseUrl, owner.identity, vt.token, "public/hello.txt");
    expect(await pub.text()).toBe("public hi");

    await expect(
      downloadFile(relay.baseUrl, owner.identity, vt.token, "private/secret.txt"),
    ).rejects.toMatchObject({ status: 403 });

    await expect(
      uploadFile(relay.baseUrl, owner.identity, vt.token, "public/vandal.txt", "nope"),
    ).rejects.toMatchObject({ status: 403 });
  });

  it("anonymous requests are rejected", async () => {
    const res = await fetch(`${relay.baseUrl}/dav/${owner.identity}/public/hello.txt`);
    expect(res.status).toBe(401);
  });

  it("owner cannot overwrite relay-managed id.json via DAV", async () => {
    await expect(
      uploadFile(relay.baseUrl, owner.identity, ownerToken, "poweur-sys/public/id.json", "{}"),
    ).rejects.toMatchObject({ status: 403 });
  });

  it("parseMultistatus handles encoded hrefs and sorts dirs first", () => {
    const xml = `<?xml version="1.0"?>
      <D:multistatus xmlns:D="DAV:">
        <D:response><D:href>/dav/me.poweur.net/docs/</D:href>
          <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
        </D:response>
        <D:response><D:href>/dav/me.poweur.net/docs/b%20file.txt</D:href>
          <D:propstat><D:prop><D:resourcetype/><D:getcontentlength>5</D:getcontentlength>
          <D:getetag>"abc"</D:getetag></D:prop></D:propstat>
        </D:response>
        <D:response><D:href>/dav/me.poweur.net/docs/sub/</D:href>
          <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
        </D:response>
      </D:multistatus>`;
    const entries = parseMultistatus(xml, "docs");
    expect(entries.map((e) => e.name)).toEqual(["sub", "b file.txt"]);
    expect(entries[0].dir).toBe(true);
    expect(entries[1].size).toBe(5);
    expect(entries[1].etag).toBe('"abc"');
  });

  it("fmtBytes renders human sizes", () => {
    expect(fmtBytes(0)).toBe("0 B");
    expect(fmtBytes(512)).toBe("512 B");
    expect(fmtBytes(2048)).toBe("2.0 KB");
    expect(fmtBytes(5 * 1024 * 1024)).toBe("5.0 MB");
  });
});
