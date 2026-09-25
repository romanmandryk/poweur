/**
 * Poweur's browser telemetry: Grafana Faro, first-party and privacy-first.
 *
 * Two tiers:
 *   - Always: page views (a screen name, never a URL), errors and web vitals,
 *     with every ID, domain and email scrubbed (scrub.ts), no user, no session
 *     and nothing stored in the browser. No consent is needed for that.
 *   - After the user opts in (`setIdentity(id)`): the same signals carry the
 *     Poweur ID. Secrets in URLs are still removed.
 *
 * Signals go to the page's own origin (`/faro/collect`), which Caddy routes to
 * Alloy's faro.receiver: no third party sees them. The instrumentations are
 * chosen by hand: user-action, navigation, performance, console and session
 * tracking are left out because they record element text, URLs or store IDs.
 */
import {
  BaseTransport,
  ErrorsInstrumentation,
  initializeFaro,
  ViewInstrumentation,
  WebVitalsInstrumentation,
  type Faro,
} from "@grafana/faro-web-sdk";
import { scrubItem, scrubText, type ScrubbableItem } from "./scrub";

export { REDACTED_EMAIL, REDACTED_ID, scrubItem, scrubText, scrubValue } from "./scrub";
export type { ScrubbableItem } from "./scrub";

export interface FaroOptions {
  /** Collector endpoint; relative URLs resolve against the page. */
  url: string;
  /** "poweur-web", "poweur-site", "poweur-docs", "poweur-oauth". */
  app: string;
  version?: string;
  environment?: string;
  /**
   * The page's URL as reported. Defaults to the path (no host, query or
   * fragment); pass a stable name where paths carry tokens (/t/<txn>/…).
   */
  pageUrl?: () => string;
  /** For tests: skip the network, receive what would be sent. */
  transport?: (item: ScrubbableItem) => void;
}

export interface Telemetry {
  /** A page or screen was shown. Pass a stable name, not a URL. */
  pageView(name: string): void;
  /** A named action with short scalar attributes. */
  event(name: string, attributes?: Record<string, string | number | boolean>): void;
  error(error: unknown, context?: Record<string, string>): void;
  /** Attach the Poweur ID (the user opted in), or `null` to go back to anonymous. */
  setIdentity(identity: string | null): void;
  /** Whether signals currently carry an ID. */
  readonly identified: boolean;
}

const noopTelemetry: Telemetry = {
  pageView() {},
  event() {},
  error() {},
  setIdentity() {},
  identified: false,
};

let active: Telemetry = noopTelemetry;

/** The running instance, or a no-op before `startFaro` (or where it is off). */
export function telemetry(): Telemetry {
  return active;
}

/** Test seam. */
export function resetTelemetryForTests() {
  active = noopTelemetry;
}

/** Stringify short scalar attributes the way Faro events require. */
function attributesOf(attributes?: Record<string, string | number | boolean>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(attributes ?? {}).slice(0, 12)) {
    out[key] = String(value).slice(0, 120);
  }
  return out;
}

/**
 * Start Faro once per page. Returns the no-op without a URL or a browser, so
 * callers never need to check whether telemetry is configured.
 */
export function startFaro(options: FaroOptions): Telemetry {
  if (!options.url || typeof window === "undefined") return noopTelemetry;
  let identity: string | null = null;
  const url = new URL(options.url, window.location.href).toString();

  const faro: Faro = initializeFaro({
    url,
    app: {
      name: options.app,
      ...(options.version ? { version: options.version } : {}),
      environment: options.environment ?? "production",
    },
    instrumentations: [new ErrorsInstrumentation(), new WebVitalsInstrumentation(), new ViewInstrumentation()],
    sessionTracking: { enabled: false },
    trackResources: false,
    trackGeolocation: false,
    webVitalsInstrumentation: { trackAttributionSources: false },
    // Screen names, not URLs: an identity host is itself a Poweur ID.
    pageTracking: { generatePageId: () => "page" },
    isolate: true,
    preventGlobalExposure: true,
    ...(options.transport ? { transports: [new CallbackTransport(options.transport)] } : {}),
    beforeSend: (item: any) => {
      const scrubbed = scrubItem(item, identity !== null) as any;
      // The page URL says which identity host this is; keep only its path.
      if (scrubbed.meta?.page) scrubbed.meta.page = { ...scrubbed.meta.page, url: scrubText((options.pageUrl ?? pagePath)(), identity !== null) };
      return scrubbed;
    },
  } as any);

  const api = faro.api;
  active = {
    pageView(name) {
      api.setView({ name });
      api.pushEvent("page_view", { screen: name.slice(0, 80) });
    },
    event(name, attributes) {
      api.pushEvent(name, attributesOf(attributes));
    },
    error(error, context) {
      const err = error instanceof Error ? error : new Error(String(error));
      api.pushError(err, context ? { context } : undefined);
    },
    setIdentity(next) {
      identity = next;
      if (next) api.setUser({ id: next });
      else api.resetUser();
    },
    get identified() {
      return identity !== null;
    },
  };
  return active;
}

/** Hands items to a callback instead of the network (tests). */
class CallbackTransport extends BaseTransport {
  readonly name = "@poweur/faro:callback";
  readonly version = "1";
  constructor(private readonly callback: (item: ScrubbableItem) => void) {
    super();
  }
  send(items: unknown): void {
    [items].flat().forEach((item) => this.callback(item as ScrubbableItem));
  }
}

/** The page's path without its host, query or fragment. */
function pagePath(): string {
  try {
    return window.location.pathname;
  } catch {
    return "";
  }
}
