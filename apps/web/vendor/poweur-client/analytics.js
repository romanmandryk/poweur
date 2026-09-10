import { PoweurError } from "./errors.js";
export const ANALYTICS_PATH = "poweur-sys/relay/analytics.json";
export async function readAnalyticsPreference(dav) {
    const raw = await dav.readOptional(ANALYTICS_PATH);
    if (!raw)
        return null;
    const p = JSON.parse(raw);
    if (p.version !== 1 || typeof p.granted !== "boolean" || !Number.isFinite(Date.parse(p.updated_at))) {
        throw new PoweurError("invalid_document", "invalid analytics preference");
    }
    return p;
}
export async function writeAnalyticsPreference(dav, granted) {
    if (typeof granted !== "boolean")
        throw new PoweurError("invalid_argument", "consent must be boolean");
    const p = { version: 1, granted, updated_at: new Date().toISOString() };
    await dav.writeJson(ANALYTICS_PATH, p);
    return p;
}
