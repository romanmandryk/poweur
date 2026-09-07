/**
 * A minimal TOML reader/writer covering exactly what the Go CLI writes into
 * `~/.poweur/`: a single flat table of strings, booleans, integers and
 * RFC 3339 datetimes. No arrays, no nested tables, no multi-line strings.
 *
 * This exists so the TypeScript CLI can share `config.toml` and
 * `sessions/<id>.toml` with the Go CLI rather than keeping a parallel store.
 * It must round-trip with github.com/pelletier/go-toml/v2 — in particular,
 * datetimes are written *unquoted* (a quoted value would come back as a
 * string and fail Go's `time.Time` unmarshal).
 */

export type TomlValue = string | boolean | number | Date;

const DATETIME = /^\d{4}-\d{2}-\d{2}[Tt ]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})?$/;

function unescape(value: string): string {
  return value.replace(/\\(["\\nrt])/g, (_, ch: string) => {
    switch (ch) {
      case "n": return "\n";
      case "r": return "\r";
      case "t": return "\t";
      default: return ch;
    }
  });
}

function parseValue(raw: string): TomlValue {
  const value = raw.trim();
  if (value.startsWith("'") && value.endsWith("'") && value.length >= 2) {
    return value.slice(1, -1); // literal string: no escapes
  }
  if (value.startsWith('"') && value.endsWith('"') && value.length >= 2) {
    return unescape(value.slice(1, -1));
  }
  if (value === "true") return true;
  if (value === "false") return false;
  if (DATETIME.test(value)) return new Date(value.replace(" ", "T"));
  if (/^[+-]?\d+$/.test(value)) return Number(value);
  if (/^[+-]?\d*\.\d+([eE][+-]?\d+)?$/.test(value)) return Number(value);
  return value;
}

/**
 * Parse a flat TOML table. Section headers are read but their keys are
 * flattened as `section.key`, which is enough for the files we share and
 * keeps the surface small.
 */
export function parseToml(text: string): Record<string, TomlValue> {
  const out: Record<string, TomlValue> = {};
  let prefix = "";
  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (trimmed === "" || trimmed.startsWith("#")) continue;
    const section = /^\[([^\]]+)\]$/.exec(trimmed);
    if (section) {
      prefix = `${(section[1] as string).trim()}.`;
      continue;
    }
    const eq = trimmed.indexOf("=");
    if (eq < 0) continue;
    const key = trimmed.slice(0, eq).trim().replace(/^["']|["']$/g, "");
    out[prefix + key] = parseValue(stripComment(trimmed.slice(eq + 1).trim()));
  }
  return out;
}

/**
 * Drop a trailing `# comment`, without cutting a `#` that sits inside a
 * quoted value. Quoted values end at their closing quote; everything after it
 * is comment.
 */
function stripComment(raw: string): string {
  const quote = raw[0];
  if (quote !== '"' && quote !== "'") {
    const hash = raw.indexOf("#");
    return hash >= 0 ? raw.slice(0, hash).trim() : raw;
  }
  for (let i = 1; i < raw.length; i++) {
    // Basic strings honour backslash escapes; literal strings do not.
    if (quote === '"' && raw[i] === "\\") {
      i++;
      continue;
    }
    if (raw[i] === quote) return raw.slice(0, i + 1);
  }
  return raw;
}

function formatValue(value: TomlValue): string {
  if (value instanceof Date) {
    // Unquoted RFC 3339 with second precision — Go's time.Time encoding.
    return value.toISOString().replace(/\.\d{3}Z$/, "Z");
  }
  if (typeof value === "boolean") return value ? "true" : "false";
  if (typeof value === "number") return String(value);
  return JSON.stringify(value);
}

/** Serialize a flat table. Keys are emitted in insertion order. */
export function stringifyToml(table: Record<string, TomlValue | undefined>): string {
  const lines: string[] = [];
  for (const [key, value] of Object.entries(table)) {
    if (value === undefined) continue;
    lines.push(`${key} = ${formatValue(value)}`);
  }
  return `${lines.join("\n")}\n`;
}

export function tomlString(table: Record<string, TomlValue>, key: string, fallback = ""): string {
  const value = table[key];
  return typeof value === "string" ? value : fallback;
}

export function tomlBool(table: Record<string, TomlValue>, key: string, fallback = false): boolean {
  const value = table[key];
  return typeof value === "boolean" ? value : fallback;
}

export function tomlDate(table: Record<string, TomlValue>, key: string): Date | null {
  const value = table[key];
  if (value instanceof Date) return value;
  if (typeof value === "string") {
    const parsed = new Date(value);
    if (!Number.isNaN(parsed.getTime())) return parsed;
  }
  return null;
}
