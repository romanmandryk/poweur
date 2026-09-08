/**
 * Stage the web client into the shell's www/ (EPIC-019 E19-T1).
 *
 * The shell wraps the *same* tree the relay serves — there is no mobile build
 * of the UI and no fork of a screen. Copying rather than symlinking is
 * deliberate: `cap sync` follows this directory into the native projects, and a
 * symlink there produces a native bundle that works on the developer's machine
 * and nowhere else.
 */
import { cp, rm, mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const mobile = join(here, "..");
const web = join(mobile, "../web");
const www = join(mobile, "www");

// Everything the browser loads from /app/, and nothing else: no tests, no
// node_modules, no config that only means something to a dev server.
const INCLUDE = ["index.html", "js", "css", "vendor", "assets", "manifest.webmanifest", "favicon.ico"];

await rm(www, { recursive: true, force: true });
await mkdir(www, { recursive: true });

const staged = [];
for (const entry of INCLUDE) {
  try {
    await cp(join(web, entry), join(www, entry), { recursive: true });
    staged.push(entry);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
}

if (!staged.includes("index.html")) {
  throw new Error("apps/web/index.html is missing — nothing to wrap");
}

// The import map in index.html is relative (`./vendor/…`), which is what makes
// the same file work at `/app/` on a relay and at `capacitor://localhost/` here.
// Fail loudly rather than shipping a bundle whose modules 404 on a device.
const html = await readFile(join(www, "index.html"), "utf8");
const absolute = [...html.matchAll(/(?:src|href)\s*=\s*"(\/app\/[^"]*)"/g)].map((m) => m[1]);
const mapped = [...html.matchAll(/"(\/app\/[^"]*)"\s*[,}]/g)].map((m) => m[1]);
if (absolute.length || mapped.length) {
  throw new Error(
    `index.html resolves ${[...absolute, ...mapped].join(", ")} from the server root; ` +
      "the shell serves from the bundle root, so these must be relative",
  );
}
await writeFile(join(www, ".staged"), staged.join("\n") + "\n");

console.log(`staged ${staged.length} entries into apps/mobile/www/`);
