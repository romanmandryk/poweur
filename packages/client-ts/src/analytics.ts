/** Relay-owned settings over existing owner-authenticated DAV. No browser tracker. */
import type { SystemFiles } from "./systemfiles.js";
import { PoweurError } from "./errors.js";
export const ANALYTICS_PATH = ".poweur/relay/analytics.json";
export interface AnalyticsPreference { version: 1; granted: boolean; updated_at: string }
export async function readAnalyticsPreference(files: SystemFiles): Promise<AnalyticsPreference | null> {
  const raw = await files.readOptional(ANALYTICS_PATH);
  if (!raw) return null;
  const p = JSON.parse(raw) as AnalyticsPreference;
  if (p.version !== 1 || typeof p.granted !== "boolean" || !Number.isFinite(Date.parse(p.updated_at))) {
    throw new PoweurError("invalid_document", "invalid analytics preference");
  }
  return p;
}
export async function writeAnalyticsPreference(files: SystemFiles, granted: boolean): Promise<AnalyticsPreference> {
  if (typeof granted !== "boolean") throw new PoweurError("invalid_argument", "consent must be boolean");
  const p: AnalyticsPreference = { version: 1, granted, updated_at: new Date().toISOString() };
  await files.writeJson(ANALYTICS_PATH, p);
  return p;
}
