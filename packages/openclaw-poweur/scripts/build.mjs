// Bundles the plugin into dist/ with @poweur/client inside, so the published
// package is self-contained: no symlinked workspace dependency for OpenClaw's
// install-time scan to refuse, and no wait for an SDK release. openclaw is the
// host and zod must be the same instance the host uses, so both stay external.
import { build } from "esbuild";
import { rmSync } from "node:fs";

rmSync("dist", { recursive: true, force: true });
await build({
  entryPoints: ["index.ts", "setup-entry.ts"],
  outdir: "dist",
  splitting: true,
  chunkNames: "chunks/[name]-[hash]",
  bundle: true,
  platform: "node",
  format: "esm",
  target: "node22",
  external: ["openclaw", "openclaw/*", "zod"],
  // Some bundled CommonJS code calls require() for Node built-ins.
  banner: { js: "import { createRequire as __poweurCreateRequire } from 'node:module'; const require = __poweurCreateRequire(import.meta.url);" },
  legalComments: "linked",
  logLevel: "warning",
});
