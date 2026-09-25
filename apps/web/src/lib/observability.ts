/**
 * Browser observability facade over @poweur/faro (Grafana Faro, first-party).
 * The relay says where to send it in GET /app/observability.json, so it can be
 * turned off or moved without a rebuild; no Faro provider there loads nothing.
 *
 * Two tiers, as in the relay's own logs (EPIC-013):
 *   - Always: screen names, named actions, errors and web vitals, with every
 *     Poweur ID, domain and email scrubbed and nothing stored in the browser.
 *   - When the unlocked identity opts in (Settings → "Include my ID in
 *     diagnostics", the relay-analytics preference): the same signals carry
 *     the ID. Locking, switching identity or opting out drops it at once.
 * Never put message text, seeds, tokens or URL hashes into events — claim
 * fragments carry wrapped keys. The scrubber removes them anyway.
 */
import { resetTelemetryForTests, startFaro, telemetry } from "@poweur/faro";
import { APP_VERSION } from "../build-info";
import { defaultRelayUrl, isShellRuntime } from "./storage.js";

export type ObservabilityProvider = {
  type: string;
  /** Faro collector; relative to the relay. */
  url?: string;
};

export type ObservabilityConfig = {
  environment?: string;
  release?: string;
  providers: ObservabilityProvider[];
};

export type Observability = {
  track(event: string, data?: Record<string, unknown>): void;
  pageChange(name: string): void;
  captureError(error: unknown, extra?: Record<string, unknown>): void;
};

const facade: Observability = {
  track(event, data) {
    telemetry().event(event, { runtime: runtime(), ...sanitizeEventData(data) } as Record<string, string | number | boolean>);
  },
  pageChange(name) {
    telemetry().pageView(name);
  },
  captureError(error, extra) {
    const context: Record<string, string> = { runtime: runtime() };
    for (const [key, value] of Object.entries(sanitizeEventData(extra))) context[key] = String(value);
    telemetry().error(error, context);
  },
};

let consentedIdentity: string | null = null;

const runtime = () => (isShellRuntime() ? "shell" : "web");

/** Used by the error boundary and action catch-paths; no-ops until start() finishes. */
export function getObservability(): Observability {
  return facade;
}

/** Named UI action. Payloads are filtered — never pass plaintext or hashes. */
export function trackAction(event: string, data?: Record<string, unknown>): void {
  facade.track(event, data);
}

/** Test seam. */
export function resetObservabilityForTests() {
  consentedIdentity = null;
  resetTelemetryForTests();
}

export function screenName(page: string, sub: string | null): string {
  return sub ? `${page}/${sub}` : page;
}

/** Relative on /app/; absolute to the relay inside the Capacitor shell. */
export function observabilityConfigUrl(): string {
  if (!isShellRuntime()) return "observability.json";
  const relay = defaultRelayUrl().replace(/\/$/, "");
  return relay ? `${relay}/app/observability.json` : "observability.json";
}

export function loadConfig(source: string | ObservabilityConfig): Promise<ObservabilityConfig> {
  if (typeof source !== "string") return Promise.resolve(source);
  return fetch(source, { cache: "no-store" })
    .then(async (res) => {
      if (!res.ok) return { providers: [] };
      const body = (await res.json()) as ObservabilityConfig;
      return { environment: body.environment, release: body.release, providers: Array.isArray(body.providers) ? body.providers : [] };
    })
    .catch(() => ({ providers: [] }));
}

/**
 * Where the collector is. On a relay-served page a relative URL is the page's
 * own origin; the shell has no such origin, so it goes to the relay.
 */
export function collectorUrl(provider: ObservabilityProvider): string {
  const url = String(provider.url ?? "").trim();
  if (!url) return "";
  if (/^https?:\/\//i.test(url) || !isShellRuntime()) return url;
  const relay = defaultRelayUrl().replace(/\/$/, "");
  return relay ? `${relay}/${url.replace(/^\//, "")}` : "";
}

const BLOCKED_KEYS = /^(plain|body|text|seed|hash|token|secret|password|key|href|url|intro|petname|caption|fragment|peer|to|recipient|audience)/i;

/** Drop secrets and free-form content; keep short scalars (kind, outcome, screen). */
export function sanitizeEventData(data?: Record<string, unknown>): Record<string, unknown> {
  if (!data) return {};
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(data)) {
    if (BLOCKED_KEYS.test(key)) continue;
    if (typeof value === "string") {
      out[key] = value.slice(0, 80);
    } else if (typeof value === "number" || typeof value === "boolean") {
      out[key] = value;
    }
    if (Object.keys(out).length >= 8) break;
  }
  return out;
}

/** Read the collector config and start the anonymous tier. */
export async function startObservability(
  source: string | ObservabilityConfig = observabilityConfigUrl(),
): Promise<Observability> {
  const cfg = await loadConfig(source);
  const faro = cfg.providers.find((provider) => provider.type === "faro" && provider.url);
  const url = faro ? collectorUrl(faro) : "";
  if (!url) return facade;
  startFaro({
    url,
    app: "poweur-web",
    // cfg.release is the relay's version; this is the app's.
    version: APP_VERSION,
    environment: cfg.environment || "production",
  });
  // Uncaught errors and unhandled rejections are Faro's ErrorsInstrumentation.
  // Consent may have arrived while the config was loading.
  if (consentedIdentity) telemetry().setIdentity(consentedIdentity);
  facade.track("app-start");
  return facade;
}

/**
 * The unlocked identity's choice to include its ID in diagnostics. Anything
 * but a grant for a named identity goes back to anonymous.
 */
export function setAnalyticsConsent(identity: string | null, granted: boolean): void {
  consentedIdentity = identity && granted ? identity : null;
  telemetry().setIdentity(consentedIdentity);
}

/** Keys closed or another identity chosen: consent belongs to one identity. */
export function syncIdentifiedUser(identity: string | null, unlocked: boolean): void {
  if (!identity || !unlocked || identity !== consentedIdentity) setAnalyticsConsent(null, false);
}
