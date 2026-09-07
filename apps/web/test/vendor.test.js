/**
 * The served static tree is what a browser actually loads, and it has no
 * bundler and no CDN: `pnpm vendor` copies `@poweur/client` and its `@noble/*`
 * dependencies into `vendor/`, rewriting bare specifiers to relative paths.
 *
 * These assertions are about that copy, not about the package — a stale or
 * Node-flavoured vendor tree breaks the app with a blank page and no error
 * anywhere the test suite would otherwise look.
 */
import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const WEB_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");
const VENDOR = join(WEB_DIR, "vendor");

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) out.push(...walk(full));
    else out.push(full);
  }
  return out;
}

describe("vendored @poweur/client", () => {
  it("is present and up to date with the built package", () => {
    expect(existsSync(join(VENDOR, "poweur-client/index.js"))).toBe(true);
    // Throws (non-zero exit) when vendor/ differs from a fresh run.
    execFileSync("node", ["scripts/vendor.mjs", "--check"], { cwd: WEB_DIR, stdio: "pipe" });
  });

  it("resolves without an import map or a bundler", async () => {
    const client = await import(join(VENDOR, "poweur-client/index.js"));
    expect(typeof client.PoweurClient).toBe("function");
    expect(typeof client.solvePow).toBe("function");
    expect(client.PROTOCOL_VERSION).toBeTruthy();

    const browser = await import(join(VENDOR, "poweur-client/browser/index.js"));
    expect(typeof browser.dohTxtResolver).toBe("function");
  });

  it("carries no bare specifiers and no Node builtins", () => {
    for (const file of walk(VENDOR).filter((f) => f.endsWith(".js"))) {
      for (const line of readFileSync(file, "utf8").split("\n")) {
        if (/^\s*(\*|\/\/|\/\*)/.test(line)) continue;
        const match = /\bfrom\s*["']([^"']+)["']/.exec(line);
        if (!match) continue;
        expect(match[1], `${file} imports ${match[1]}`).toMatch(/^\.{1,2}\//);
      }
    }
  });

  it("is wired into index.html relative to the document base, with no CDN", () => {
    const html = readFileSync(join(WEB_DIR, "index.html"), "utf8");
    expect(html).toContain('"@poweur/client": "./vendor/poweur-client/index.js"');
    expect(html).not.toContain("esm.sh");
    // Absolute "/app/…" would bind the app to one mount point; the relay serves
    // it at /app/, a static server at /, and the EPIC-019 shell at
    // capacitor://localhost/.
    expect(html).not.toContain('"/app/vendor');
  });
});
