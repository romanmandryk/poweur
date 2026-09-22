/**
 * Browser observability facade. Providers are chosen at runtime from
 * GET /app/observability.json so a vendor can be added or removed without a
 * rebuild. Unset BETTERSTACK_RUM_TOKEN (empty providers) loads nothing.
 *
 * Do not put identities, message text, or URL hashes into events — claim
 * fragments carry wrapped keys.
 */
import { defaultRelayUrl, isShellRuntime } from "./storage.js";
export type ObservabilityProvider = {
  type: string;
  token: string;
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

const noop: Observability = { track() {}, pageChange() {}, captureError() {} };

let current: Observability = noop;

/** Used by the error boundary and action catch-paths; no-ops until start() finishes. */
export function getObservability(): Observability {
  return current;
}

let errorsAbort: AbortController | undefined;

function installGlobalErrorHandlers(api: Observability) {
  errorsAbort?.abort();
  errorsAbort = new AbortController();
  const { signal } = errorsAbort;
  window.addEventListener(
    "error",
    (event) => {
      api.captureError(event.error ?? event.message, { source: "window" });
    },
    { signal },
  );
  window.addEventListener(
    "unhandledrejection",
    (event) => {
      api.captureError(event.reason, { source: "rejection" });
    },
    { signal },
  );
}

/** Test seam. */
export function resetObservabilityForTests() {
  errorsAbort?.abort();
  errorsAbort = undefined;
  current = noop;
}

type BetterstackFn = {
  (...args: unknown[]): void;
  q?: unknown[];
  l?: number;
};

declare global {
  interface Window {
    betterstack?: BetterstackFn;
  }
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
  return fetch(source, { cache: "no-store" }).then(async (res) => {
    if (!res.ok) return { providers: [] };
    const body = (await res.json()) as ObservabilityConfig;
    return { environment: body.environment, release: body.release, providers: Array.isArray(body.providers) ? body.providers : [] };
  }).catch(() => ({ providers: [] }));
}

function installBetterStack(token: string, environment: string, release: string) {
  const w = window;
  const fn: BetterstackFn = w.betterstack || function (...args: unknown[]) {
    (fn.q = fn.q || []).push(args);
  };
  w.betterstack = fn;
  fn.l = Date.now();
  const script = document.createElement("script");
  script.async = true;
  script.crossOrigin = "anonymous";
  script.src = "https://betterstack.net/b.js?t=" + encodeURIComponent(token);
  try {
    (document.head || document.documentElement).appendChild(script);
  } catch {
    // happy-dom and some CSPs refuse the remote script; init still queues.
  }
  fn("init", { environment, release, autoPageview: false });
}

export function errorPayload(error: unknown, extra?: Record<string, unknown>): Record<string, unknown> {
  const err = error instanceof Error ? error : new Error(String(error));
  return {
    name: err.name.slice(0, 80),
    message: String(err.message).slice(0, 200),
    stack: String(err.stack ?? "").split("\n").slice(0, 12).join("\n"),
    ...extra,
  };
}

export async function startObservability(
  source: string | ObservabilityConfig = observabilityConfigUrl(),
): Promise<Observability> {
  const cfg = await loadConfig(source);
  const environment = cfg.environment || "production";
  const release = cfg.release || "";
  for (const provider of cfg.providers) {
    if (provider.type === "betterstack" && provider.token) {
      installBetterStack(provider.token, environment, release);
    }
  }
  const runtime = isShellRuntime() ? "shell" : "web";
  const api: Observability = {
    track(event, data) {
      window.betterstack?.("track", event, data ?? {});
    },
    pageChange(name) {
      window.betterstack?.("track", "page-change", { name, runtime });
    },
    captureError(error, extra) {
      const payload = errorPayload(error, { runtime, ...extra });
      window.betterstack?.("track", "error", payload);
      const sentry = (window as Window & { Sentry?: { captureException?: (e: unknown) => void } }).Sentry;
      sentry?.captureException?.(error);
    },
  };
  if (!cfg.providers.some((p) => p.type === "betterstack" && p.token)) {
    current = noop;
    return noop;
  }
  current = api;
  installGlobalErrorHandlers(api);
  api.track("app-start", { runtime });
  return api;
}
