/**
 * Vendor `@poweur/client` (and the `@noble/*` modules it reaches) into the
 * static tree the relay serves.
 *
 * The web app has no bundler and EPIC-015 keeps it that way, so this is a copy
 * step, not a build: every file that lands in `vendor/` is the same ESM the
 * package publishes. The one transformation is specifier rewriting — bare
 * specifiers (`@noble/hashes/sha2.js`) become relative paths, so the import map
 * in index.html needs a single entry for the package itself rather than one per
 * transitive subpath.
 *
 *   node scripts/vendor.mjs           # write vendor/
 *   node scripts/vendor.mjs --check   # fail if vendor/ is stale (CI)
 */
import { createRequire } from "node:module";
import { dirname, join, posix, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import {
  existsSync, mkdirSync, readFileSync, readdirSync,
  rmSync, statSync, writeFileSync,
} from "node:fs";

const here = dirname(fileURLToPath(import.meta.url));
const WEB_DIR = resolve(here, "..");
const REPO_ROOT = resolve(WEB_DIR, "../..");
const OUT_DIR = join(WEB_DIR, "vendor");
const require = createRequire(join(REPO_ROOT, "packages/client-ts/package.json"));

const CHECK = process.argv.includes("--check");

// ─── Package registry ─────────────────────────────────────────────────────────

/**
 * A vendored package: where its ESM lives on disk, where it lands under
 * vendor/, and the `exports` map used to resolve bare subpath specifiers.
 */
function pkg(name, rootDir, outName, exportsMap) {
  return { name, rootDir, outName, exports: exportsMap };
}

/** `@noble/*` packages do not export `./package.json`, so walk up from the main entry. */
function nobleDir(name) {
  let dir = dirname(require.resolve(name));
  while (!existsSync(join(dir, "package.json"))) {
    const parent = dirname(dir);
    if (parent === dir) throw new Error(`cannot locate package root for ${name}`);
    dir = parent;
  }
  return dir;
}

function noblePkg(name, outName) {
  const dir = nobleDir(name);
  const manifest = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
  return pkg(name, join(dir, "esm"), outName, manifest.exports);
}

const PACKAGES = [
  pkg(
    "@poweur/client",
    join(REPO_ROOT, "packages/client-ts/dist"),
    "poweur-client",
    JSON.parse(readFileSync(join(REPO_ROOT, "packages/client-ts/package.json"), "utf8")).exports,
  ),
  noblePkg("@noble/ciphers", "noble-ciphers"),
  noblePkg("@noble/curves", "noble-curves"),
  noblePkg("@noble/hashes", "noble-hashes"),
];

const byName = new Map(PACKAGES.map((p) => [p.name, p]));

/** Entry points the app is allowed to import. Everything else arrives transitively. */
const ENTRIES = [
  ["@poweur/client", "."],
  ["@poweur/client", "./browser"],
];

// ─── Resolution ───────────────────────────────────────────────────────────────

/** Pick the browser-facing target from an `exports` entry, ignoring `node`. */
function pickCondition(value) {
  if (typeof value === "string") return value;
  if (!value || typeof value !== "object") return null;
  for (const key of ["browser", "import", "module", "default"]) {
    if (key in value) {
      const picked = pickCondition(value[key]);
      if (picked) return picked;
    }
  }
  return null;
}

/**
 * Resolve `subpath` inside a package to a file under its ESM root.
 * Packages disagree about whether subpaths carry `.js`, so try both spellings.
 */
function resolveSubpath(target, subpath) {
  const candidates = [subpath];
  if (subpath !== "." && subpath.endsWith(".js")) candidates.push(subpath.slice(0, -3));
  else if (subpath !== ".") candidates.push(`${subpath}.js`);

  for (const candidate of candidates) {
    const entry = target.exports?.[candidate];
    const picked = entry ? pickCondition(entry) : null;
    if (!picked) continue;
    // exports targets are relative to the package root; our root is esm/ for
    // noble, dist/ for the client — strip whichever prefix the map used.
    const abs = resolve(target.rootDir, "..", picked.replace(/^\.\//, ""));
    const fromRoot = relative(target.rootDir, abs);
    if (!fromRoot.startsWith("..") && existsSync(abs)) return fromRoot.split(sep).join("/");
    // The map pointed outside our ESM root (a CJS twin); fall back to the same
    // basename under the root.
    const inRoot = join(target.rootDir, picked.replace(/^\.\//, ""));
    if (existsSync(inRoot)) return relative(target.rootDir, inRoot).split(sep).join("/");
  }

  // No exports entry (or none that resolved): treat the subpath as a file path.
  for (const candidate of candidates) {
    if (candidate === ".") continue;
    const abs = join(target.rootDir, candidate.replace(/^\.\//, ""));
    if (existsSync(abs) && statSync(abs).isFile()) {
      return relative(target.rootDir, abs).split(sep).join("/");
    }
  }
  throw new Error(`cannot resolve ${target.name}${subpath === "." ? "" : `/${subpath.slice(2)}`}`);
}

/** Split "@noble/hashes/sha2.js" into ["@noble/hashes", "./sha2.js"]. */
function splitBare(spec) {
  const parts = spec.split("/");
  const name = spec.startsWith("@") ? parts.slice(0, 2).join("/") : parts[0];
  const rest = spec.slice(name.length);
  return [name, rest ? `.${rest}` : "."];
}

/** Resolve a relative specifier against the importing file, inside one package. */
function resolveRelative(target, fromFile, spec) {
  const base = posix.dirname(fromFile);
  const joined = posix.normalize(posix.join(base, spec));
  const candidates = [joined];
  if (joined.endsWith(".ts")) candidates.push(`${joined.slice(0, -3)}.js`);
  if (!joined.endsWith(".js")) candidates.push(`${joined}.js`);
  candidates.push(posix.join(joined, "index.js"));
  for (const candidate of candidates) {
    const abs = join(target.rootDir, candidate);
    if (existsSync(abs) && statSync(abs).isFile()) return candidate;
  }
  throw new Error(`cannot resolve ${spec} from ${target.name}/${fromFile}`);
}

// ─── Graph walk ───────────────────────────────────────────────────────────────

const SPECIFIER_RE = /(\bfrom\s*|\bimport\s*|\bexport\s*\*\s*from\s*|\bimport\()(["'])([^"']+)\2/g;

const emitted = new Map(); // "pkgName\0fileFromRoot" -> rewritten source
const queue = [];

function enqueue(pkgName, file) {
  const key = `${pkgName}\0${file}`;
  if (emitted.has(key)) return;
  emitted.set(key, null);
  queue.push({ pkgName, file });
}

for (const [name, subpath] of ENTRIES) {
  const target = byName.get(name);
  enqueue(name, resolveSubpath(target, subpath));
}

/** Where a vendored file lands, as a path under vendor/. */
function outPath(pkgName, file) {
  return posix.join(byName.get(pkgName).outName, file);
}

/** Rewrite one import specifier, enqueueing whatever it points at. */
function rewriteSpecifier(pkgName, file, spec) {
  const target = byName.get(pkgName);
  let depPkg;
  let depFile;
  if (spec.startsWith(".")) {
    depPkg = pkgName;
    depFile = resolveRelative(target, file, spec);
  } else if (byName.has(splitBare(spec)[0])) {
    const [name, subpath] = splitBare(spec);
    depPkg = name;
    depFile = resolveSubpath(byName.get(name), subpath);
  } else if (spec.startsWith("node:")) {
    throw new Error(`${pkgName}/${file} imports ${spec} — not loadable in a browser`);
  } else {
    throw new Error(`${pkgName}/${file} imports unvendored package ${spec}`);
  }

  enqueue(depPkg, depFile);
  const rel = posix.relative(posix.dirname(outPath(pkgName, file)), outPath(depPkg, depFile));
  return rel.startsWith(".") ? rel : `./${rel}`;
}

const COMMENT_LINE_RE = /^\s*(\*|\/\/|\/\*)/;

while (queue.length) {
  const { pkgName, file } = queue.shift();
  const source = readFileSync(join(byName.get(pkgName).rootDir, file), "utf8");

  // Line-oriented, skipping comment lines: the package's own doc comments quote
  // specifiers as usage examples, and those must not be rewritten or followed.
  const rewritten = source
    .split("\n")
    .map((line) =>
      COMMENT_LINE_RE.test(line)
        ? line
        : line.replace(SPECIFIER_RE, (_match, head, quote, spec) =>
            `${head}${quote}${rewriteSpecifier(pkgName, file, spec)}${quote}`),
    )
    .join("\n")
    // The .map files are not vendored, so the pragma would only produce 404s.
    .replace(/^\/\/# sourceMappingURL=.*$/gm, "");

  emitted.set(`${pkgName}\0${file}`, rewritten);
}

// ─── Write / check ────────────────────────────────────────────────────────────

const files = new Map(); // path under vendor/ -> contents
for (const [key, contents] of emitted) {
  const [pkgName, file] = key.split("\0");
  files.set(outPath(pkgName, file), contents);
}
for (const [name, outName] of [["@poweur/client", "poweur-client"]]) {
  const manifest = JSON.parse(
    readFileSync(join(byName.get(name).rootDir, "..", "package.json"), "utf8"),
  );
  files.set(
    posix.join(outName, "VENDORED.json"),
    `${JSON.stringify({ name, version: manifest.version, generatedBy: "apps/web/scripts/vendor.mjs" }, null, 2)}\n`,
  );
}

function listExisting(dir, prefix = "") {
  if (!existsSync(dir)) return [];
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const child = posix.join(prefix, entry.name);
    if (entry.isDirectory()) out.push(...listExisting(join(dir, entry.name), child));
    else out.push(child);
  }
  return out;
}

if (CHECK) {
  const existing = new Set(listExisting(OUT_DIR));
  const problems = [];
  for (const [path, contents] of files) {
    if (!existing.has(path)) problems.push(`missing: ${path}`);
    else if (readFileSync(join(OUT_DIR, path), "utf8") !== contents) problems.push(`stale: ${path}`);
    existing.delete(path);
  }
  for (const extra of existing) problems.push(`unexpected: ${extra}`);
  if (problems.length) {
    console.error("vendor/ is out of date — run `pnpm vendor` in apps/web:");
    for (const problem of problems.slice(0, 20)) console.error(`  ${problem}`);
    if (problems.length > 20) console.error(`  … and ${problems.length - 20} more`);
    process.exit(1);
  }
  console.log(`vendor/ is up to date (${files.size} files)`);
} else {
  rmSync(OUT_DIR, { recursive: true, force: true });
  for (const [path, contents] of files) {
    const dest = join(OUT_DIR, path);
    mkdirSync(dirname(dest), { recursive: true });
    writeFileSync(dest, contents);
  }
  console.log(`vendored ${files.size} files into apps/web/vendor/`);
}
