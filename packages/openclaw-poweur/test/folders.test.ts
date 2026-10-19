import { describe, expect, it } from "vitest";
import { conflictName, folderContext, planSync } from "../src/folders.js";

const m = <T>(o: Record<string, T>) => new Map(Object.entries(o));

describe("planSync", () => {
  const last = { "a.md": { version: "v1", hash: "h1" } };

  it("does nothing when neither side changed", () => {
    expect(planSync(m({ "a.md": { version: "v1" } }), m({ "a.md": { hash: "h1" } }), last, true)).toEqual([]);
  });

  it("downloads the owner's changes and uploads the agent's", () => {
    expect(planSync(m({ "a.md": { version: "v2" } }), m({ "a.md": { hash: "h1" } }), last, true)).toEqual([{ kind: "download", path: "a.md" }]);
    expect(planSync(m({ "a.md": { version: "v1" } }), m({ "a.md": { hash: "h2" } }), last, true)).toEqual([{ kind: "upload", path: "a.md", create: false }]);
  });

  it("keeps both on a conflict, and never writes with a read-only share", () => {
    expect(planSync(m({ "a.md": { version: "v2" } }), m({ "a.md": { hash: "h2" } }), last, true)).toEqual([{ kind: "conflict", path: "a.md" }]);
    expect(planSync(m({ "a.md": { version: "v2" } }), m({ "a.md": { hash: "h2" } }), last, false)).toEqual([{ kind: "download", path: "a.md" }]);
    expect(planSync(m({}), m({ "new.md": { hash: "x" } }), {}, false)).toEqual([]);
  });

  it("creates the agent's new files and fetches the owner's", () => {
    expect(planSync(m({ "in.md": { version: "v1" } }), m({ "out.md": { hash: "x" } }), {}, true)).toEqual([
      { kind: "download", path: "in.md" },
      { kind: "upload", path: "out.md", create: true },
    ]);
  });

  it("follows the owner's deletions but never deletes the owner's files", () => {
    expect(planSync(m({}), m({ "a.md": { hash: "h1" } }), last, true)).toEqual([{ kind: "delete-local", path: "a.md" }]);
    expect(planSync(m({ "a.md": { version: "v1" } }), m({}), last, true)).toEqual([{ kind: "download", path: "a.md" }]);
    expect(planSync(m({}), m({}), last, true)).toEqual([{ kind: "forget", path: "a.md" }]);
  });
});

describe("helpers", () => {
  it("names conflict copies beside the original", () => {
    expect(conflictName("notes/report.md")).toBe("notes/report (agent).md");
    expect(conflictName("README")).toBe("README (agent)");
  });

  it("tells the agent where each folder is and what it may do", () => {
    const text = folderContext([{ drive: "alice.poweur.net", node: "n", name: "Plan", role: "write", shareId: "s", local: "/w/shared/alice.poweur.net/Plan" }], "/w");
    expect(text).toContain("shared/alice.poweur.net/Plan");
    expect(text).toContain("you can add and edit files");
    expect(folderContext([], "/w")).toBe("");
  });
});
