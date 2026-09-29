/**
 * Relay HTTP transport. One place that knows how the relay reports errors,
 * so every module above surfaces the same typed failures.
 */

import { sha256 } from "@noble/hashes/sha2.js";
import type { Signer } from "./crypto/keys.js";
import { ChallengeRequiredError, RelayError } from "./errors.js";

/**
 * Proof for a signed request: the relay accepts it without a challenge round
 * trip. The signature covers identity, method, path and query, a timestamp,
 * a nonce and the body's SHA-256, so it can be neither altered nor replayed.
 */
export interface RequestSigning {
  signer: Signer;
  /** Signed by a session key rather than the identity key. */
  sessionId?: string;
}

const hex = (bytes: Uint8Array) => Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");

async function bodyBytes(body: BodyInit | undefined): Promise<Uint8Array> {
  if (body === undefined) return new Uint8Array();
  if (typeof body === "string") return new TextEncoder().encode(body);
  if (body instanceof Uint8Array) return body;
  if (body instanceof ArrayBuffer) return new Uint8Array(body);
  if (typeof Blob !== "undefined" && body instanceof Blob) return new Uint8Array(await body.arrayBuffer());
  throw new Error("cannot sign this request body");
}

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

export interface RequestOptions {
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
  /** `error` refuses redirects (presigned uploads must not follow one). */
  redirect?: RequestRedirect;
  /**
   * A long-lived response (the push stream) opts out of the request timeout —
   * a stream that is *supposed* to stay open all day must not be killed after
   * thirty seconds of quiet.
   */
  stream?: boolean;
  /** Sign the request itself instead of fetching a challenge first. */
  sign?: RequestSigning;
}

export class RelayClient {
  readonly relayUrl: string;
  readonly #fetch: FetchLike;
  readonly #timeoutMs: number;
  readonly #headers: Record<string, string>;
  /** Relay clock minus ours, from response `Date` headers: signed requests
   * carry the relay's time so a device with a wrong clock is not refused. */
  #clockOffsetMs = 0;

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
      const url = /^https?:\/\//.test(options.path) ? options.path : this.relayUrl + options.path;
      const send = async () => {
        if (options.sign) Object.assign(headers, await this.#signature(options.sign, options.method, url, body));
        const response = await this.#fetch(url, {
          method: options.method,
          headers,
          ...(body !== undefined ? { body } : {}),
          redirect: options.redirect,
          signal: controller.signal,
        });
        return response;
      };
      let response = await send();
      const before = this.#clockOffsetMs;
      this.#noteClock(response);
      // Refused because our clock is off: now that we know the relay's time,
      // sign once more.
      if (options.sign && response.status === 401 && Math.abs(this.#clockOffsetMs - before) > 30_000) {
        await response.body?.cancel().catch(() => {});
        response = await send();
      }
      return response;
    } finally {
      if (timer !== null) clearTimeout(timer);
      options.signal?.removeEventListener("abort", abort);
    }
  }

  #noteClock(response: Response): void {
    const date = response.headers?.get?.("Date");
    const relay = date ? Date.parse(date) : NaN;
    if (Number.isFinite(relay)) this.#clockOffsetMs = relay - Date.now();
  }

  async #signature(sign: RequestSigning, method: string, url: string, body: BodyInit | undefined): Promise<Record<string, string>> {
    const target = new URL(url);
    const timestamp = String(Math.floor((Date.now() + this.#clockOffsetMs) / 1000));
    const nonce = hex(globalThis.crypto.getRandomValues(new Uint8Array(16)));
    const message = ["poweur-request/v1", sign.signer.identity, method.toUpperCase(), target.pathname + target.search, timestamp, nonce, hex(sha256(await bodyBytes(body)))].join("\n");
    return {
      "X-Poweur-Identity": sign.signer.identity,
      "X-Poweur-Timestamp": timestamp,
      "X-Poweur-Nonce": nonce,
      "X-Poweur-Signature": await sign.signer.sign(message, "base64std"),
      ...(sign.sessionId ? { "X-Poweur-Session-Id": sign.sessionId } : {}),
    };
  }

  /** Perform a request and decode JSON, mapping failures to typed errors. */
  async request<T>(options: RequestOptions): Promise<T> {
    // Say so. `GET /` is both the relay's root document and the launcher's
    // front door, and a relay serving a web UI redirects readers who did not
    // ask for the document — so a client that stayed silent got HTML back and
    // could not tell which front door it was standing in. Only this path
    // decodes JSON; `raw()` also carries downloads and streams.
    const response = await this.raw({
      ...options,
      headers: { Accept: "application/json", ...options.headers },
    });
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
