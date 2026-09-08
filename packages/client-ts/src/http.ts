/**
 * Relay HTTP transport. One place that knows how the relay reports errors,
 * so every module above surfaces the same typed failures.
 */

import { ChallengeRequiredError, RelayError } from "./errors.js";

export type FetchLike = typeof globalThis.fetch;

/**
 * The ambient `fetch`, bound to its global.
 *
 * Browsers enforce the receiver: `const f = globalThis.fetch; f(url)` throws
 * "Illegal invocation" in a page while working fine in Node. Every default
 * fetch in this package therefore goes through here.
 */
export function defaultFetch(): FetchLike {
  return globalThis.fetch.bind(globalThis);
}

export interface RelayClientOptions {
  fetch?: FetchLike;
  timeoutMs?: number;
  /** Extra headers on every request (e.g. a tracing id). */
  headers?: Record<string, string>;
}

export function trimRelayUrl(relayUrl: string): string {
  return relayUrl.replace(/\/+$/, "");
}

/** Host[:port] of a relay URL — what canonical strings bind against. */
export function relayAddressFromUrl(relayUrl: string): string {
  let trimmed = relayUrl.replace(/^https?:\/\//, "");
  const slash = trimmed.indexOf("/");
  if (slash >= 0) trimmed = trimmed.slice(0, slash);
  return trimmed;
}

interface RequestOptions {
  method: string;
  path: string;
  body?: unknown;
  headers?: Record<string, string>;
  /** Return the raw Response instead of parsed JSON. */
  raw?: boolean;
  /** Statuses to treat as success beyond 2xx (e.g. 207 for PROPFIND). */
  allowStatus?: number[];
  /** Caller's abort signal, honoured alongside the client's own timeout. */
  signal?: AbortSignal;
  /**
   * A long-lived response (the push stream) opts out of the request timeout —
   * a stream that is *supposed* to stay open all day must not be killed after
   * thirty seconds of quiet.
   */
  stream?: boolean;
}

export class RelayClient {
  readonly relayUrl: string;
  readonly #fetch: FetchLike;
  readonly #timeoutMs: number;
  readonly #headers: Record<string, string>;

  constructor(relayUrl: string, options: RelayClientOptions = {}) {
    this.relayUrl = trimRelayUrl(relayUrl);
    this.#fetch = options.fetch ?? defaultFetch();
    this.#timeoutMs = options.timeoutMs ?? 30_000;
    this.#headers = options.headers ?? {};
  }

  get address(): string {
    return relayAddressFromUrl(this.relayUrl);
  }

  async raw(options: RequestOptions): Promise<Response> {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (options.signal?.aborted) abort();
    options.signal?.addEventListener("abort", abort, { once: true });
    const timer = options.stream ? null : setTimeout(abort, this.#timeoutMs);
    const headers: Record<string, string> = { ...this.#headers, ...options.headers };
    let body: BodyInit | undefined;
    if (options.body !== undefined) {
      if (
        typeof options.body === "string" ||
        options.body instanceof Uint8Array ||
        options.body instanceof ArrayBuffer ||
        (typeof Blob !== "undefined" && options.body instanceof Blob)
      ) {
        body = options.body as BodyInit;
      } else {
        body = JSON.stringify(options.body);
        headers["Content-Type"] ??= "application/json";
      }
    }
    try {
      return await this.#fetch(this.relayUrl + options.path, {
        method: options.method,
        headers,
        ...(body !== undefined ? { body } : {}),
        signal: controller.signal,
      });
    } finally {
      if (timer !== null) clearTimeout(timer);
      options.signal?.removeEventListener("abort", abort);
    }
  }

  /** Perform a request and decode JSON, mapping failures to typed errors. */
  async request<T>(options: RequestOptions): Promise<T> {
    const response = await this.raw(options);
    const ok =
      response.ok || (options.allowStatus?.includes(response.status) ?? false);
    const text = await response.text();
    let data: unknown;
    try {
      data = text ? JSON.parse(text) : undefined;
    } catch {
      data = undefined;
    }
    if (!ok) {
      throw toRelayError(response.status, text, data, options.method, options.path);
    }
    return data as T;
  }
}

/** Map a relay error body onto the right typed error. */
export function toRelayError(
  status: number,
  text: string,
  data: unknown,
  method = "",
  path = "",
): Error {
  const record = (data ?? {}) as Record<string, unknown>;

  // 428 carries the anonymous-sender challenge envelope (EPIC-014).
  if (status === 428 && record["challenge"]) {
    const challenge = record["challenge"] as Record<string, unknown>;
    return new ChallengeRequiredError({
      type: String(challenge["type"] ?? ""),
      algo: challenge["algo"] as string | undefined,
      token: String(challenge["token"] ?? ""),
      bits: Number(challenge["bits"] ?? 0),
      expires_at: challenge["expires_at"] as string | undefined,
      detail: challenge["detail"] as string | undefined,
    });
  }

  const relayCode = typeof record["error"] === "string" ? (record["error"] as string) : undefined;
  const detail = typeof record["detail"] === "string" ? (record["detail"] as string) : undefined;
  const where = method && path ? `${method} ${path}` : "relay request";
  const message =
    detail ?? relayCode ?? (text.trim() || `${where} failed with HTTP ${status}`);
  return new RelayError(message, {
    status,
    ...(detail !== undefined ? { detail } : {}),
    ...(relayCode !== undefined ? { relayCode } : {}),
  });
}
