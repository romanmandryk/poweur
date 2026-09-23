/**
 * The request code carried by `poweur://auth?request=…`.
 * The approve screen stores and checks that code, not the link.
 * `https://` links are not claimed by the shell and are ignored here.
 */
export function signInCodeFromAppUrl(url: string): string | null {
  const value = String(url ?? "").trim();
  if (!value.toLowerCase().startsWith("poweur:")) return null;

  const fromUrl = codeFromParsedUrl(value);
  if (fromUrl) return fromUrl;

  // A WebView that cannot parse this scheme still hands us the literal link.
  const match = value.match(/^poweur:(?:\/\/)?auth\?request=([^&#\s]+)/i);
  if (!match) return null;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return match[1];
  }
}

/** A pasted poweur://auth link becomes its request code. Any other text is unchanged. */
export function signInCodeFromInput(input: string): string {
  const value = String(input ?? "").trim();
  return signInCodeFromAppUrl(value) ?? value;
}

function codeFromParsedUrl(value: string): string | null {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return null;
  }
  if (parsed.protocol !== "poweur:") return null;
  const path = parsed.pathname.replace(/^\/+/, "");
  const auth = parsed.hostname === "auth" || path === "auth" || path.startsWith("auth/");
  if (!auth) return null;
  return parsed.searchParams.get("request") || null;
}
