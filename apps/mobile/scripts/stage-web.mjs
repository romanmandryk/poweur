/**
 * Stage the web client into the shell's www/ (EPIC-019 E19-T1).
 *
 * The shell wraps the *same* tree the relay serves — there is no mobile build
 * of the UI and no fork of a screen. Copying rather than symlinking is
 * deliberate: `cap sync` follows this directory into the native projects, and a
 * symlink there produces a native bundle that works on the developer's machine
 * and nowhere else.
 *
 * `WEB_SOURCE=next` stages the EPIC-021 React build (`apps/web-next/dist`)
 * instead of the legacy tree, until the cutover makes it the only one (E21-T14).
 */
import { cp, rm, mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const mobile = join(here, "..");
const www = join(mobile, "www");
const source = process.env.WEB_SOURCE === "next" ? "next" : "legacy";

await rm(www, { recursive: true, force: true });
await mkdir(www, { recursive: true });

const staged = source === "next" ? await stageNext() : await stageLegacy();

if (!staged.includes("index.html")) {
  throw new Error(`${source === "next" ? "apps/web-next/dist" : "apps/web"}/index.html is missing — nothing to wrap`);
}

// Everything must resolve relative to the document: the same file works at
// `/app/` on a relay and at `capacitor://localhost/` here. Fail loudly rather
// than shipping a bundle whose modules 404 on a device.
const html = await readFile(join(www, "index.html"), "utf8");
const rootAbsolute = source === "next" ? /(?:src|href)\s*=\s*"(\/[^"]*)"/g : /(?:src|href)\s*=\s*"(\/app\/[^"]*)"/g;
const absolute = [...html.matchAll(rootAbsolute)].map((m) => m[1]);
const mapped = [...html.matchAll(/"(\/app\/[^"]*)"\s*[,}]/g)].map((m) => m[1]);
if (absolute.length || mapped.length) {
  throw new Error(
    `index.html resolves ${[...absolute, ...mapped].join(", ")} from the server root; ` +
      "the shell serves from the bundle root, so these must be relative",
  );
}
await writeFile(join(www, ".staged"), `source: ${source}\n${staged.join("\n")}\n`);

console.log(`staged ${staged.length} entries (${source}) into apps/mobile/www/`);

/** Everything the browser loads from /app/, and nothing else. */
async function stageLegacy() {
  const web = join(mobile, "../web");
  // No tests, no node_modules, no config that only means something to a dev server.
  const INCLUDE = ["index.html", "js", "css", "vendor", "assets", "manifest.webmanifest", "favicon.ico"];
  const done = [];
  for (const entry of INCLUDE) {
    try {
      await cp(join(web, entry), join(www, entry), { recursive: true });
      done.push(entry);
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
  }
  return done;
}

/** The Vite build output, minus source maps: a device has no use for them. */
async function stageNext() {
  const dist = join(mobile, "../web-next/dist");
  if (!existsSync(join(dist, "index.html"))) {
    throw new Error("apps/web-next/dist is missing — run `pnpm web-next:build` first");
  }
  const done = [];
  for (const entry of await readdir(dist)) {
    await cp(join(dist, entry), join(www, entry), {
      recursive: true,
      filter: (path) => !path.endsWith(".map"),
    });
    done.push(entry);
  }
  return done;
}
