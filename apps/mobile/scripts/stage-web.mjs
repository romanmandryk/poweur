/**
 * Stage the web client into the shell's www/ (EPIC-019 E19-T1).
 *
 * The shell wraps the *same* build the relay serves at /app/ — there is no
 * mobile build of the UI and no fork of a screen. Copying rather than
 * symlinking is deliberate: `cap sync` follows this directory into the native
 * projects, and a symlink there produces a native bundle that works on the
 * developer's machine and nowhere else.
 */
import { cp, rm, mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const mobile = join(here, "..");
const www = join(mobile, "www");
const dist = join(mobile, "../web/dist");

if (!existsSync(join(dist, "index.html"))) {
  throw new Error("apps/web/dist/index.html is missing — build the web app first (`pnpm web`)");
}

await rm(www, { recursive: true, force: true });
await mkdir(www, { recursive: true });

// The Vite output, minus source maps: a device has no use for them.
const staged = [];
for (const entry of await readdir(dist)) {
  await cp(join(dist, entry), join(www, entry), {
    recursive: true,
    filter: (path) => !path.endsWith(".map"),
  });
  staged.push(entry);
}

// Everything must resolve relative to the document: the same build works at
// `/app/` on a relay and at `capacitor://localhost/` here. Fail loudly rather
// than shipping a bundle whose assets 404 on a device.
const html = await readFile(join(www, "index.html"), "utf8");
const absolute = [...html.matchAll(/(?:src|href)\s*=\s*"(\/[^"]*)"/g)].map((m) => m[1]);
if (absolute.length) {
  throw new Error(
    `index.html resolves ${absolute.join(", ")} from the server root; ` +
      "the shell serves from the bundle root, so these must be relative",
  );
}
await writeFile(join(www, ".staged"), `${staged.join("\n")}\n`);

console.log(`staged ${staged.length} entries from apps/web/dist into apps/mobile/www/`);
