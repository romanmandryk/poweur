/**
 * A `fetch` that dials a fixed address while keeping the request's own Host
 * header — the equivalent of the Go CLI's `POWEUR_RESOLVER_DIAL`.
 *
 * Identity resolution asks for `https://alice.poweur.net/.well-known/…`, but a
 * local relay has no DNS zone pointing at it. The Go CLI solves this by
 * overriding the dial address while leaving the URL (and therefore the Host
 * header, which virtual hosting depends on) untouched. WHATWG `fetch` cannot
 * express that — `Host` is a forbidden header — so this drops to `node:http`.
 *
 * Development and integration only; production resolves through real DNS.
 */

import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";

export interface DialFetchOptions {
  /** `host[:port]` to connect to instead of the URL's host. */
  dial: string;
  timeoutMs?: number;
}

/** Build a fetch-compatible function pinned to one dial address. */
export function dialFetch(options: DialFetchOptions): typeof globalThis.fetch {
  const [dialHost, dialPort] = options.dial.includes(":")
    ? options.dial.split(":")
    : [options.dial, undefined];

  return (async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(typeof input === "string" ? input : input.toString());
    const secure = url.protocol === "https:";
    const send = secure ? httpsRequest : httpRequest;

    const headers: Record<string, string> = {};
    new Headers(init.headers ?? {}).forEach((value, key) => {
      headers[key] = value;
    });
    // The Host header is the whole point: the relay routes virtual hosts on it.
    headers["host"] = url.host;

    return new Promise<Response>((resolve, reject) => {
      const req = send(
        {
          host: dialHost,
          port: dialPort ? Number(dialPort) : secure ? 443 : 80,
          path: `${url.pathname}${url.search}`,
          method: init.method ?? "GET",
          headers,
          // The certificate belongs to the identity, not the dial address.
          ...(secure ? { servername: url.hostname } : {}),
          timeout: options.timeoutMs ?? 5_000,
        },
        (res) => {
          const chunks: Buffer[] = [];
          res.on("data", (chunk: Buffer) => chunks.push(chunk));
          res.on("end", () => {
            const status = res.statusCode ?? 502;
            // `redirect: "error"` is how the resolver refuses redirects; honour
            // it here rather than silently following one.
            if (init.redirect === "error" && status >= 300 && status < 400) {
              reject(new Error("redirects are not allowed when resolving identity documents"));
              return;
            }
            const responseHeaders = new Headers();
            for (const [key, value] of Object.entries(res.headers)) {
              if (typeof value === "string") responseHeaders.set(key, value);
              else if (Array.isArray(value)) responseHeaders.set(key, value.join(", "));
            }
            resolve(
              new Response(Buffer.concat(chunks), {
                status,
                headers: responseHeaders,
              }),
            );
          });
        },
      );
      req.on("timeout", () => req.destroy(new Error("resolver request timed out")));
      req.on("error", reject);
      if (init.body !== undefined && init.body !== null) {
        req.write(typeof init.body === "string" ? init.body : Buffer.from(init.body as ArrayBuffer));
      }
      req.end();
    });
  }) as unknown as typeof globalThis.fetch;
}
