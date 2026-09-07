/**
 * Argument parsing for the CLI.
 *
 * Deliberately hand-rolled and dependency-free, matching the Go CLI's accepted
 * shapes: `--flag value`, `--flag=value`, `-flag`, bool flags with no value,
 * and flags interleaved with positionals anywhere on the line (Go achieves the
 * last one by reordering args before handing them to `flag`).
 */

export interface FlagSpec {
  /** Flags that take no value. */
  bool?: string[];
  /** Flags that may repeat, collected into an array. */
  repeatable?: string[];
  /** Defaults applied when a flag is absent. */
  defaults?: Record<string, string | boolean>;
}

export interface ParsedArgs {
  flags: Record<string, string | boolean>;
  lists: Record<string, string[]>;
  positional: string[];
}

export class UsageError extends Error {}

export function parseArgs(argv: string[], spec: FlagSpec = {}): ParsedArgs {
  const bools = new Set(spec.bool ?? []);
  const repeatable = new Set(spec.repeatable ?? []);
  const flags: Record<string, string | boolean> = { ...spec.defaults };
  const lists: Record<string, string[]> = {};
  const positional: string[] = [];

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i] as string;
    if (arg === "--") {
      positional.push(...argv.slice(i + 1));
      break;
    }
    if (!arg.startsWith("-") || arg === "-") {
      positional.push(arg);
      continue;
    }
    const withoutDashes = arg.replace(/^--?/, "");
    const eq = withoutDashes.indexOf("=");
    const name = eq >= 0 ? withoutDashes.slice(0, eq) : withoutDashes;
    let value: string | boolean;
    if (eq >= 0) {
      value = withoutDashes.slice(eq + 1);
    } else if (bools.has(name)) {
      value = true;
    } else {
      const next = argv[i + 1];
      if (next === undefined || (next.startsWith("-") && next !== "-")) {
        throw new UsageError(`flag --${name} needs a value`);
      }
      value = next;
      i++;
    }
    if (repeatable.has(name)) {
      (lists[name] ??= []).push(String(value));
      continue;
    }
    flags[name] = value;
  }

  return { flags, lists, positional };
}

export function flagString(args: ParsedArgs, name: string, fallback = ""): string {
  const value = args.flags[name];
  return typeof value === "string" ? value : fallback;
}

export function flagBool(args: ParsedArgs, name: string): boolean {
  const value = args.flags[name];
  return value === true || value === "true" || value === "1";
}

export function flagNumber(args: ParsedArgs, name: string, fallback = 0): number {
  const value = args.flags[name];
  if (typeof value !== "string" || value.trim() === "") return fallback;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

/** Comma-separated list flag (`--members a,b,c`), trimmed and de-blanked. */
export function flagList(args: ParsedArgs, name: string): string[] {
  return flagString(args, name)
    .split(",")
    .map((value) => value.trim())
    .filter((value) => value !== "");
}

/** Repeatable flag (`--with a --with b`). */
export function repeated(args: ParsedArgs, name: string): string[] {
  return args.lists[name] ?? [];
}

export function requirePositional(args: ParsedArgs, index: number, usage: string): string {
  const value = args.positional[index];
  if (value === undefined || value === "") throw new UsageError(usage);
  return value;
}
