import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { APP_VERSION } from "../src/build-info";
import pkg from "../package.json";

const SRC = join(import.meta.dirname, "../src");

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? walk(path) : [path];
  });
}

describe("scaffold (E21-T1)", () => {
  it("build-info matches package.json", () => {
    expect(APP_VERSION).toBe(pkg.version);
  });

  it("never builds HTML from strings", () => {
    // Remote profiles and typed identities reach the UI; string-built HTML is
    // how an injection lands.
    const offenders = walk(SRC)
      .filter((f) => /\.(t|j)sx?$/.test(f))
      .filter((f) => /dangerouslySetInnerHTML|\.innerHTML\s*=|insertAdjacentHTML/.test(readFileSync(f, "utf8")));
    expect(offenders).toEqual([]);
  });

  it("has no hand-written CSS besides index.css", () => {
    const css = walk(SRC).filter((f) => f.endsWith(".css"));
    expect(css.map((f) => f.slice(SRC.length + 1))).toEqual(["index.css"]);
  });
});
