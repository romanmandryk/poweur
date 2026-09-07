#!/usr/bin/env node
/** Executable entry point: `poweur` / `npx @poweur/client`. */

import { run } from "./index.js";

const code = await run(process.argv.slice(2));
process.exitCode = code;
