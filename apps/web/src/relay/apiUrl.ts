/** API origin: same host as the SPA in production (under /app); dev uses Vite proxy (empty origin). */
export function apiOrigin(): string {
  return typeof window !== 'undefined' ? window.location.origin : '';
}

export function relayBaseFromInput(relayUrl: string): string {
  return relayUrl.replace(/\/+$/, '');
}

export function relayHostPortFromUrl(relayURL: string): string {
  let t = relayURL.trim().replace(/^https?:\/\//, '');
  const slash = t.indexOf('/');
  if (slash >= 0) t = t.slice(0, slash);
  return t;
}
