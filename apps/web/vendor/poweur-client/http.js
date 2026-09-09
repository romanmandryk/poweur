/**
 * Relay HTTP transport. One place that knows how the relay reports errors,
 * so every module above surfaces the same typed failures.
 */
import { ChallengeRequiredError, RelayError } from "./errors.js";
/**
 * The ambient `fetch`, bound to its global.
 *
 * Browsers enforce the receiver: `const f = globalThis.fetch; f(url)` throws
 * "Illegal invocation" in a page while working fine in Node. Every default
 * fetch in this package therefore goes through here.
 */
export function defaultFetch() {
    return globalThis.fetch.bind(globalThis);
}
export function trimRelayUrl(relayUrl) {
    return relayUrl.replace(/\/+$/, "");
}
/** Host[:port] of a relay URL — what canonical strings bind against. */
export function relayAddressFromUrl(relayUrl) {
    let trimmed = relayUrl.replace(/^https?:\/\//, "");
    const slash = trimmed.indexOf("/");
    if (slash >= 0)
        trimmed = trimmed.slice(0, slash);
    return trimmed;
}
export class RelayClient {
    relayUrl;
    #fetch;
    #timeoutMs;
    #headers;
    constructor(relayUrl, options = {}) {
        this.relayUrl = trimRelayUrl(relayUrl);
        this.#fetch = options.fetch ?? defaultFetch();
        this.#timeoutMs = options.timeoutMs ?? 30_000;
        this.#headers = options.headers ?? {};
    }
    get address() {
        return relayAddressFromUrl(this.relayUrl);
    }
    async raw(options) {
        const controller = new AbortController();
        const abort = () => controller.abort();
        if (options.signal?.aborted)
            abort();
        options.signal?.addEventListener("abort", abort, { once: true });
        const timer = options.stream ? null : setTimeout(abort, this.#timeoutMs);
        const headers = { ...this.#headers, ...options.headers };
        let body;
        if (options.body !== undefined) {
            if (typeof options.body === "string" ||
                options.body instanceof Uint8Array ||
                options.body instanceof ArrayBuffer ||
                (typeof Blob !== "undefined" && options.body instanceof Blob)) {
                body = options.body;
            }
            else {
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
        }
        finally {
            if (timer !== null)
                clearTimeout(timer);
            options.signal?.removeEventListener("abort", abort);
        }
    }
    /** Perform a request and decode JSON, mapping failures to typed errors. */
    async request(options) {
        // Say so. `GET /` is both the relay's root document and the launcher's
        // front door, and a relay serving a web UI redirects readers who did not
        // ask for the document — so a client that stayed silent got HTML back and
        // could not tell which front door it was standing in. Only this path
        // decodes JSON; `raw()` also carries downloads and streams.
        const response = await this.raw({
            ...options,
            headers: { Accept: "application/json", ...options.headers },
        });
        const ok = response.ok || (options.allowStatus?.includes(response.status) ?? false);
        const text = await response.text();
        let data;
        try {
            data = text ? JSON.parse(text) : undefined;
        }
        catch {
            data = undefined;
        }
        if (!ok) {
            throw toRelayError(response.status, text, data, options.method, options.path);
        }
        return data;
    }
}
/** Map a relay error body onto the right typed error. */
export function toRelayError(status, text, data, method = "", path = "") {
    const record = (data ?? {});
    // 428 carries the anonymous-sender challenge envelope (EPIC-014).
    if (status === 428 && record["challenge"]) {
        const challenge = record["challenge"];
        return new ChallengeRequiredError({
            type: String(challenge["type"] ?? ""),
            algo: challenge["algo"],
            token: String(challenge["token"] ?? ""),
            bits: Number(challenge["bits"] ?? 0),
            expires_at: challenge["expires_at"],
            detail: challenge["detail"],
        });
    }
    const relayCode = typeof record["error"] === "string" ? record["error"] : undefined;
    const detail = typeof record["detail"] === "string" ? record["detail"] : undefined;
    const where = method && path ? `${method} ${path}` : "relay request";
    const message = detail ?? relayCode ?? (text.trim() || `${where} failed with HTTP ${status}`);
    return new RelayError(message, {
        status,
        ...(detail !== undefined ? { detail } : {}),
        ...(relayCode !== undefined ? { relayCode } : {}),
    });
}
