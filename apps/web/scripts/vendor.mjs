/**
 * Vendor `@poweur/client` and everything it reaches into the static tree the
 * relay serves.
 *
 * The web app has no bundler and EPIC-015 keeps it that way, so this is a copy
 * step, not a build: every file that lands in `vendor/` is the same ESM the
 * package publishes. The one transformation is specifier rewriting — bare
 * specifiers (`@noble/hashes/sha2.js`) become relative paths, so the import map
 * in index.html needs one entry for the package itself rather than one per
 * transitive subpath.
 *
 * Resolution is **per importing package**, not global. `@scure/bip39` needs
 * `@noble/hashes@2`, while `@poweur/client` is on `@noble/hashes@1`; resolving
 * a bare specifier from the wrong place silently produces a tree that throws at
 * first import. Two versions of one package therefore get two directories.
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
const CLIENT_DIR = join(REPO_ROOT, "packages/client-ts");

const CHECK = process.argv.includes("--check");

// ─── Package registry, keyed by resolved directory ───────────────────────────

/** absolute package dir → { name, version, rootDir, outName, exports } */
const packages = new Map();
/** Output directory names already taken, so a second version gets its own. */
const usedOutNames = new Set();

function outNameFor(name, version) {
  const base = name.replace(/^@/, "").replace(/\//g, "-");
  if (!usedOutNames.has(base)) {
    usedOutNames.add(base);
    return base;
  }
  const versioned = `${base}@${version}`;
  usedOutNames.add(versioned);
  return versioned;
}

/**
 * Register a package directory. `rootDir` is where its ESM lives: `esm/` when
 * the package ships dual CJS/ESM that way, otherwise the package root.
 */
function register(dir) {
  const existing = packages.get(dir);
  if (existing) return existing;

  const manifest = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
  const rootDir = existsSync(join(dir, "esm")) ? join(dir, "esm") : dir;
  const entry = {
    name: manifest.name,
    version: manifest.version,
    dir,
    rootDir,
    exports: manifest.exports,
    outName: outNameFor(manifest.name, manifest.version),
    require: createRequire(join(dir, "package.json")),
  };
  packages.set(dir, entry);
  return entry;
}

/** Walk up from a resolved file to the directory holding its package.json. */
function packageRootOf(file) {
  let dir = dirname(file);
  while (!existsSync(join(dir, "package.json"))) {
    const parent = dirname(dir);
    if (parent === dir) throw new Error(`cannot locate a package root above ${file}`);
    dir = parent;
  }
  return dir;
}

/**
 * Resolve a bare package name from `importer`'s own node_modules, which is what
 * makes nested versions work.
 */
function resolvePackage(importer, name) {
  // Some packages (the @noble family) do not export "./package.json", so
  // resolve the main entry and walk up from it.
  return register(packageRootOf(importer.require.resolve(name)));
}

// ─── Subpath resolution ──────────────────────────────────────────────────────

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
 * Resolve `subpath` inside a package to a file under its ESM root. Packages
 * disagree about whether subpaths carry `.js`, so try both spellings.
 */
function resolveSubpath(target, subpath) {
  const candidates = [subpath];
  if (subpath !== "." && subpath.endsWith(".js")) candidates.push(subpath.slice(0, -3));
  else if (subpath !== ".") candidates.push(`${subpath}.js`);

  for (const candidate of candidates) {
    const picked = target.exports?.[candidate] ? pickCondition(target.exports[candidate]) : null;
    if (!picked) continue;
    const fromPackageRoot = resolve(target.dir, picked.replace(/^\.\//, ""));
    const asRelative = relative(target.rootDir, fromPackageRoot);
    if (!asRelative.startsWith("..") && existsSync(fromPackageRoot)) {
      return asRelative.split(sep).join("/");
    }
    // The map pointed at the CJS twin outside our ESM root; take the same
    // path under the root instead.
    const inRoot = join(target.rootDir, picked.replace(/^\.\//, ""));
    if (existsSync(inRoot)) return relative(target.rootDir, inRoot).split(sep).join("/");
  }

  // No usable exports entry: treat the subpath as a plain file path.
  for (const candidate of candidates) {
    if (candidate === ".") continue;
    const abs = join(target.rootDir, candidate.replace(/^\.\//, ""));
    if (existsSync(abs) && statSync(abs).isFile()) {
      return relative(target.rootDir, abs).split(sep).join("/");
    }
  }
  throw new Error(`cannot resolve ${target.name}${subpath === "." ? "" : subpath.slice(1)}`);
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
  const joined = posix.normalize(posix.join(posix.dirname(fromFile), spec));
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

// ─── Graph walk ──────────────────────────────────────────────────────────────

const SPECIFIER_RE = /(\bfrom\s*|\bimport\s*|\bexport\s*\*\s*from\s*|\bimport\()(["'])([^"']+)\2/g;
const COMMENT_LINE_RE = /^\s*(\*|\/\/|\/\*)/;

const emitted = new Map(); // "<pkg dir>\0<file>" -> rewritten source
const queue = [];

function enqueue(pkg, file) {
  const key = `${pkg.dir}\0${file}`;
  if (emitted.has(key)) return;
  emitted.set(key, null);
  queue.push({ pkg, file });
}

/** Where a vendored file lands, as a path under vendor/. */
function outPath(pkg, file) {
  return posix.join(pkg.outName, file);
}

/** Rewrite one import specifier, enqueueing whatever it points at. */
function rewriteSpecifier(pkg, file, spec) {
  let depPkg;
  let depFile;
  if (spec.startsWith(".")) {
    depPkg = pkg;
    depFile = resolveRelative(pkg, file, spec);
  } else if (spec.startsWith("node:")) {
    throw new Error(`${pkg.name}/${file} imports ${spec} — not loadable in a browser`);
  } else {
    const [name, subpath] = splitBare(spec);
    try {
      depPkg = resolvePackage(pkg, name);
    } catch (cause) {
      throw new Error(`${pkg.name}/${file} imports ${spec}, which does not resolve: ${cause.message}`);
    }
    depFile = resolveSubpath(depPkg, subpath);
  }

  enqueue(depPkg, depFile);
  const rel = posix.relative(posix.dirname(outPath(pkg, file)), outPath(depPkg, depFile));
  return rel.startsWith(".") ? rel : `./${rel}`;
}

const client = register(CLIENT_DIR);
// The client's ESM is its build output, not the package root.
client.rootDir = join(CLIENT_DIR, "dist");

/** Entry points the app is allowed to import; everything else arrives transitively. */
for (const subpath of [".", "./browser"]) {
  enqueue(client, resolveSubpath(client, subpath));
}

while (queue.length) {
  const { pkg, file } = queue.shift();
  const source = readFileSync(join(pkg.rootDir, file), "utf8");

  // Line-oriented, skipping comment lines: the packages' own doc comments quote
  // specifiers as usage examples, and those must not be rewritten or followed.
  const rewritten = source
    .split("\n")
    .map((line) =>
      COMMENT_LINE_RE.test(line)
        ? line
        : line.replace(SPECIFIER_RE, (_match, head, quote, spec) =>
            `${head}${quote}${rewriteSpecifier(pkg, file, spec)}${quote}`),
    )
    .join("\n")
    // The .map files are not vendored, so the pragma would only produce 404s.
    .replace(/^\/\/# sourceMappingURL=.*$/gm, "");

  emitted.set(`${pkg.dir}\0${file}`, rewritten);
}

// ─── Write / check ───────────────────────────────────────────────────────────

const files = new Map(); // path under vendor/ -> contents
for (const [key, contents] of emitted) {
  const [dir, file] = key.split("\0");
  files.set(outPath(packages.get(dir), file), contents);
}
files.set(
  posix.join(client.outName, "VENDORED.json"),
  `${JSON.stringify(
    {
      name: client.name,
      version: client.version,
      generatedBy: "apps/web/scripts/vendor.mjs",
      packages: [...packages.values()]
        .map((p) => `${p.name}@${p.version}`)
        .sort(),
    },
    null,
    2,
  )}\n`,
);

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
