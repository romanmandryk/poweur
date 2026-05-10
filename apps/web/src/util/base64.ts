/** Standard base64 (matches Go json.Marshal default for signatures). */
export function encodeStd(b: Uint8Array): string {
  let s = '';
  for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]!);
  return btoa(s);
}

export function decodeStd(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export function encodeRawURL(b: Uint8Array): string {
  let s = encodeStd(b);
  s = s.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  return s;
}

/** Decode relay/CLI public keys (rawurl, url, rawstd, std). */
export function decodeAnyBase64(s: string): Uint8Array {
  const cleaned = s.trim();
  try {
    return decodeStd(cleaned);
  } catch {
    // fall through
  }
  // URL-safe base64
  const pad = '='.repeat((4 - (cleaned.length % 4)) % 4);
  const norm = (cleaned + pad).replace(/-/g, '+').replace(/_/g, '/');
  return decodeStd(norm);
}
