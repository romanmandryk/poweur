/**
 * The local sync engine against a real relay — `poweur sync pull|push|status`.
 *
 * The cases that matter are the reconciliation ones: a clean remote edit
 * applies, a simultaneous edit produces a conflicted copy rather than
 * silently losing work, and a delete on one side propagates to the other.
 */

import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import type { DavClient } from "../src/files.js";
import { SyncClient } from "../src/sync.js";
import { Ignore, SyncEngine, loadState, type SyncState } from "../src/node/syncengine.js";
import { createTestIdentity, type TestIdentity } from "./helpers/identities.js";
import { startRelay, type RunningRelay } from "./helpers/relay.js";

describe("sync engine ↔ real relay", () => {
  let relay: RunningRelay;
  let owner: TestIdentity;
  let dav: DavClient;
  let roots: string[] = [];

  beforeAll(async () => {
    relay = await startRelay();
    owner = await createTestIdentity(relay.baseUrl, "syncer");
    dav = await owner.client.dav();
  }, 180_000);

  afterAll(() => {
    relay?.stop();
    for (const root of roots) rmSync(root, { recursive: true, force: true });
  });

  /** A fresh sync root with its own engine, sharing the owner's tree. */
  function newEngine(state?: SyncState): { root: string; engine: SyncEngine } {
    const root = mkdtempSync(join(tmpdir(), "poweur-sync-"));
    roots.push(root);
    // The tree roots exist remotely already; create them locally so tests can
    // drop files straight in.
    for (const dir of ["private", "public", "shared", "apps"]) {
      mkdirSync(join(root, dir), { recursive: true });
    }
    return {
      root,
      engine: new SyncEngine({
        root,
        dav,
        sync: new SyncClient(owner.client.relay, dav.identity, dav.token),
        state: state ?? loadState(root),
        ignore: Ignore.load(root),
        device: "test-device",
      }),
    };
  }

  it("pushes local files and pulls them into a second root", async () => {
    const a = newEngine();
    mkdirSync(join(a.root, "private", "notes"), { recursive: true });
    writeFileSync(join(a.root, "private", "notes", "one.md"), "first");

    const pushed = await a.engine.push();
    expect(pushed.uploaded).toContain("private/notes/one.md");
    expect(pushed.mkdirRemote).toContain("private/notes");

    const b = newEngine();
    const pulled = await b.engine.pull();
    expect(pulled.downloaded).toContain("private/notes/one.md");
    expect(readFileSync(join(b.root, "private", "notes", "one.md"), "utf8")).toBe("first");
  });

  it("reports pending work without changing anything", async () => {
    const a = newEngine();
    await a.engine.pull();
    writeFileSync(join(a.root, "private", "status-probe.txt"), "new");

    const status = await a.engine.status();
    expect(status.localNew).toContain("private/status-probe.txt");
    // Status must not have uploaded it.
    expect(await dav.readOptional("private/status-probe.txt")).toBeNull();
  });

  it("applies a clean remote edit", async () => {
    const a = newEngine();
    await a.engine.push();
    await a.engine.pull();
    await dav.write("private/remote-edit.txt", "from elsewhere");

    const report = await a.engine.pull();
    expect(report.downloaded).toContain("private/remote-edit.txt");
    expect(readFileSync(join(a.root, "private", "remote-edit.txt"), "utf8")).toBe("from elsewhere");
    expect(report.conflicts).toHaveLength(0);
  });

  it("keeps the local version as a conflicted copy when both sides changed", async () => {
    const a = newEngine();
    await dav.write("private/contested.txt", "base");
    await a.engine.pull();
    expect(readFileSync(join(a.root, "private", "contested.txt"), "utf8")).toBe("base");

    // Both sides edit from the same base.
    writeFileSync(join(a.root, "private", "contested.txt"), "mine");
    await dav.write("private/contested.txt", "theirs");

    const report = await a.engine.pull();
    expect(report.conflicts).toHaveLength(1);
    const loser = report.conflicts[0] as string;
    expect(loser).toContain("conflicted copy from test-device");
    // The remote version wins the real path; ours survives beside it.
    expect(readFileSync(join(a.root, "private", "contested.txt"), "utf8")).toBe("theirs");
    expect(readFileSync(join(a.root, loser), "utf8")).toBe("mine");
  });

  it("propagates a local delete to the relay", async () => {
    const a = newEngine();
    writeFileSync(join(a.root, "private", "doomed.txt"), "bye");
    await a.engine.push();
    expect(await dav.readOptional("private/doomed.txt")).toBe("bye");

    rmSync(join(a.root, "private", "doomed.txt"));
    const report = await a.engine.push();
    expect(report.deletedRemote).toContain("private/doomed.txt");
    expect(await dav.readOptional("private/doomed.txt")).toBeNull();
  });

  it("propagates a remote delete to the local tree", async () => {
    const a = newEngine();
    await dav.write("private/vanishing.txt", "here");
    await a.engine.pull();
    expect(existsSync(join(a.root, "private", "vanishing.txt"))).toBe(true);

    await dav.remove("private/vanishing.txt");
    const report = await a.engine.pull();
    expect(report.deletedLocal).toContain("private/vanishing.txt");
    expect(existsSync(join(a.root, "private", "vanishing.txt"))).toBe(false);
  });

  it("keeps a locally-edited file the remote deleted", async () => {
    const a = newEngine();
    await dav.write("private/edited-then-deleted.txt", "base");
    await a.engine.pull();
    writeFileSync(join(a.root, "private", "edited-then-deleted.txt"), "my edit");
    await dav.remove("private/edited-then-deleted.txt");

    await a.engine.pull();
    // The edit wins: losing unsynced work to someone else's delete is the
    // one outcome sync must never produce.
    expect(readFileSync(join(a.root, "private", "edited-then-deleted.txt"), "utf8")).toBe("my edit");
  });

  it("honours .poweurignore and only syncs the requested roots", async () => {
    const a = newEngine();
    writeFileSync(join(a.root, ".poweurignore"), "*.tmp\n");
    mkdirSync(join(a.root, "private"), { recursive: true });
    writeFileSync(join(a.root, "private", "keep.txt"), "keep");
    writeFileSync(join(a.root, "private", "skip.tmp"), "skip");

    const engine = new SyncEngine({
      root: a.root,
      dav,
      sync: new SyncClient(owner.client.relay, dav.identity, dav.token),
      state: loadState(a.root),
      ignore: Ignore.load(a.root),
      roots: ["private"],
    });
    const report = await engine.push();
    expect(report.uploaded).toContain("private/keep.txt");
    expect(report.uploaded).not.toContain("private/skip.tmp");
    expect(await dav.readOptional("private/skip.tmp")).toBeNull();
  });

  it("does not re-upload unchanged files on a second push", async () => {
    const a = newEngine();
    writeFileSync(join(a.root, "private", "stable.txt"), "unchanged");
    await a.engine.push();
    const second = await a.engine.push();
    expect(second.uploaded).not.toContain("private/stable.txt");
  });

  it("run() is pull-then-push and reports both", async () => {
    const a = newEngine();
    await dav.write("private/for-run.txt", "remote");
    writeFileSync(join(a.root, "private", "from-run.txt"), "local");

    const { pull, push } = await a.engine.run();
    expect(pull.downloaded).toContain("private/for-run.txt");
    expect(push.uploaded).toContain("private/from-run.txt");
  });
});
