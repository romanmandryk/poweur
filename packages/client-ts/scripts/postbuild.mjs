// Adds the executable bit + shebang guard to the built CLI entry so
// `npx @poweur/client` works straight from the published tarball.
import { chmodSync, readFileSync, writeFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const bin = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "cli", "bin.js");
const source = readFileSync(bin, "utf8");
if (!source.startsWith("#!")) {
  writeFileSync(bin, "#!/usr/bin/env node\n" + source);
}
chmodSync(bin, 0o755);
