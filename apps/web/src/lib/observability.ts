/**
 * Browser observability facade. Providers are chosen at runtime from
 * GET /app/observability.json so a vendor can be added or removed without a
 * rebuild. Unset BETTERSTACK_RUM_TOKEN (empty providers) loads nothing.
 *
 * Do not put identities, message text, or URL hashes into events — claim
 * fragments carry wrapped keys.
 */
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
};

const noop: Observability = { track() {}, pageChange() {} };

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

export async function startObservability(
  source: string | ObservabilityConfig = "observability.json",
): Promise<Observability> {
  const cfg = await loadConfig(source);
  const environment = cfg.environment || "production";
  const release = cfg.release || "";
  for (const provider of cfg.providers) {
    if (provider.type === "betterstack" && provider.token) {
      installBetterStack(provider.token, environment, release);
    }
  }
  const api: Observability = {
    track(event, data) {
      window.betterstack?.("track", event, data ?? {});
    },
    pageChange(name) {
      window.betterstack?.("track", "page-change", { name });
    },
  };
  if (!cfg.providers.some((p) => p.type === "betterstack" && p.token)) return noop;
  return api;
}
