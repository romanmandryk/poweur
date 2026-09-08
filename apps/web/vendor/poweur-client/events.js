/**
 * Push notifications from the relay (EPIC-009 E09-T2).
 *
 * The stream tells you *that* something arrived, never what: every event is a
 * cue to read from your cursor, which is where delivery actually happens. That
 * asymmetry is the design — a dropped frame costs a round trip, not a message,
 * so the transport needs no delivery guarantees and reconnecting needs no
 * replay protocol.
 *
 * It is Server-Sent Events read through streamed `fetch`, not `EventSource`:
 * the relay authenticates the stream with the same challenge-signed headers as
 * the inbox pickup, and `EventSource` cannot send headers. Putting a signature
 * in the query string instead would write a credential into every access log.
 */
import { PoweurError } from "./errors.js";
/**
 * Open one stream. Resolves when the relay closes it or the signal aborts;
 * rejects only if it could not be opened.
 *
 * Callers that want it to stay open use `streamForever`.
 */
export async function streamEvents(client, signer, options) {
    const { challenge } = await client.request({
        method: "GET",
        path: `/auth/challenge?identity=${encodeURIComponent(signer.identity)}`,
    });
    const signature = await signer.sign(challenge, "base64std");
    const response = await client.raw({
        method: "GET",
        path: `/events/${encodeURIComponent(signer.identity)}`,
        headers: {
            "X-Poweur-Identity": signer.identity,
            "X-Poweur-Challenge": challenge,
            "X-Poweur-Signature": signature,
            Accept: "text/event-stream",
        },
        stream: true,
        ...(options.signal ? { signal: options.signal } : {}),
    });
    if (!response.ok || !response.body) {
        throw new PoweurError("relay_error", `stream refused (${response.status})`, {
            status: response.status,
        });
    }
    options.onOpen?.();
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    try {
        for (;;) {
            const { done, value } = await reader.read();
            if (done)
                return;
            buffer += decoder.decode(value, { stream: true });
            // SSE frames are separated by a blank line; a partial frame stays in the
            // buffer until the rest of it arrives.
            let split = buffer.indexOf("\n\n");
            while (split >= 0) {
                const frame = buffer.slice(0, split);
                buffer = buffer.slice(split + 2);
                const event = parseFrame(frame);
                if (event)
                    options.onEvent(event);
                split = buffer.indexOf("\n\n");
            }
        }
    }
    finally {
        try {
            await reader.cancel();
        }
        catch {
            /* the stream is already gone */
        }
    }
}
function parseFrame(frame) {
    const data = frame
        .split("\n")
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).trim())
        .join("\n");
    if (!data)
        return null; // a `: keepalive` comment frame
    try {
        return JSON.parse(data);
    }
    catch {
        return null;
    }
}
/**
 * Keep a stream open, reconnecting with exponential backoff until the caller
 * aborts.
 *
 * Reconnecting is cheap precisely because the stream carries no state: the
 * caller reads from its cursor on every `ready`, so a gap of any length costs
 * one extra fetch. Backoff exists to be kind to the relay, not to protect the
 * client from losing anything.
 */
export async function streamForever(client, signer, options) {
    const base = options.baseDelayMs ?? 1000;
    const max = options.maxDelayMs ?? 30_000;
    let delay = base;
    while (!options.signal?.aborted) {
        try {
            await streamEvents(client, signer, {
                onEvent: options.onEvent,
                ...(options.signal ? { signal: options.signal } : {}),
                onOpen: () => {
                    delay = base; // a successful connection resets the backoff
                    options.onOpen?.();
                },
            });
        }
        catch (error) {
            if (options.signal?.aborted)
                return;
            options.onError?.(error);
        }
        if (options.signal?.aborted)
            return;
        await sleep(delay, options.signal);
        delay = Math.min(delay * 2, max);
    }
}
function sleep(ms, signal) {
    return new Promise((resolve) => {
        const timer = setTimeout(resolve, ms);
        signal?.addEventListener("abort", () => {
            clearTimeout(timer);
            resolve();
        }, { once: true });
    });
}
