/**
 * EPIC-015 E15-T1, constraint 2: the relay base URL comes from the active
 * identity record, never from `location.origin`.
 *
 * `window.location.origin` is correct for a relay-served SPA and fatal for the
 * Capacitor shell (EPIC-019), which runs on `capacitor://localhost`. This test
 * is a source-level guard because the failure is silent — the app "works" in a
 * browser right up until it is wrapped.
 */
import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const JS_DIR = join(dirname(fileURLToPath(import.meta.url)), "..", "js");

/** The single sanctioned reader of the page origin, and why. */
const ALLOWED = new Map([["storage.js", "defaultRelayUrl()"]]);

function modules() {
  return readdirSync(JS_DIR).filter((f) => f.endsWith(".js"));
}

describe("relay URLs are identity-scoped", () => {
  it("no module but storage.js reads the page origin", () => {
    const offenders = [];
    for (const file of modules()) {
      if (ALLOWED.has(file)) continue;
      const source = readFileSync(join(JS_DIR, file), "utf8");
      for (const [index, line] of source.split("\n").entries()) {
        if (/^\s*(\*|\/\/|\/\*)/.test(line)) continue;
        if (/location\s*(\?\.)?\.origin/.test(line)) offenders.push(`${file}:${index + 1}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("storage.js confines it to defaultRelayUrl()", () => {
    const source = readFileSync(join(JS_DIR, "storage.js"), "utf8");
    const hits = source
      .split("\n")
      .map((line, i) => [line, i + 1])
      .filter(([line]) => !/^\s*(\*|\/\/|\/\*)/.test(line) && /location\s*\??\.origin/.test(line));

    expect(hits).toHaveLength(1);
    const body = source.slice(source.indexOf("export function defaultRelayUrl()"));
    expect(body.slice(0, body.indexOf("\n}")).includes("location?.origin")).toBe(true);
  });

  it("every module that talks to a relay gets its URL from client.js or storage.js", () => {
    for (const file of modules()) {
      const source = readFileSync(join(JS_DIR, file), "utf8");
      // A raw fetch() to an interpolated base URL is how origin-coupling
      // creeps back in; the protocol now lives in @poweur/client instead.
      expect(source, `${file} should not hand-roll relay HTTP`).not.toMatch(/fetch\(\s*`\$\{relayUrl/);
    }
  });
});
