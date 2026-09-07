/**
 * Browser runtime glue.
 *
 * The only thing a browser genuinely cannot do is DNS, so the DNS half of the
 * resolver goes over DNS-over-HTTPS. That is a real trust difference from the
 * CLI — the DoH provider sees which identities you look up, and it, rather
 * than your system resolver, is the one answering. The web path
 * (`/.well-known/poweur/id.json`) needs none of this and is tried first.
 */

import { defaultFetch } from "../http.js";
import type { TxtResolver } from "../resolve.js";

export const CLOUDFLARE_DOH = "https://cloudflare-dns.com/dns-query";

export interface DohOptions {
  /** DoH endpoint. Defaults to Cloudflare, matching the current web client. */
  url?: string;
  fetch?: typeof globalThis.fetch;
}

/** A TXT resolver over DNS-over-HTTPS, for browsers and edge runtimes. */
export function dohTxtResolver(options: DohOptions = {}): TxtResolver {
  const endpoint = options.url ?? CLOUDFLARE_DOH;
  const doFetch = options.fetch ?? defaultFetch();
  return {
    async lookupTxt(name: string): Promise<string[]> {
      const response = await doFetch(
        `${endpoint}?name=${encodeURIComponent(name)}&type=TXT`,
        { headers: { Accept: "application/dns-json" } },
      );
      if (!response.ok) throw new Error(`DoH lookup failed: HTTP ${response.status}`);
      const data = (await response.json()) as {
        Status?: number;
        Answer?: Array<{ data?: string }>;
      };
      if (data.Status !== 0 || !data.Answer?.length) return [];
      return data.Answer.map((record) => (record.data ?? "").replace(/^"|"$/g, ""));
    },
  };
}

/** A KeyStore over `localStorage`, for demos and low-stakes browser apps. */
export { LocalStorageKeyStore } from "./localstorage-keystore.js";
