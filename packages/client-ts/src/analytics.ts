/** Relay-owned settings over existing owner-authenticated DAV. No browser tracker. */
import type { DavClient } from "./files.js";
import { PoweurError } from "./errors.js";
export const ANALYTICS_PATH = "poweur-sys/relay/analytics.json";
export interface AnalyticsPreference { version: 1; granted: boolean; updated_at: string }
export async function readAnalyticsPreference(dav: DavClient): Promise<AnalyticsPreference | null> {
  const raw = await dav.readOptional(ANALYTICS_PATH);
  if (!raw) return null;
  const p = JSON.parse(raw) as AnalyticsPreference;
  if (p.version !== 1 || typeof p.granted !== "boolean" || !Number.isFinite(Date.parse(p.updated_at))) {
    throw new PoweurError("invalid_document", "invalid analytics preference");
  }
  return p;
}
export async function writeAnalyticsPreference(dav: DavClient, granted: boolean): Promise<AnalyticsPreference> {
  if (typeof granted !== "boolean") throw new PoweurError("invalid_argument", "consent must be boolean");
  const p: AnalyticsPreference = { version: 1, granted, updated_at: new Date().toISOString() };
  await dav.writeJson(ANALYTICS_PATH, p);
  return p;
}
