/**
 * Every default `fetch` in this package must be bound to its global.
 *
 * Browsers enforce the receiver on `fetch`: capturing `globalThis.fetch` into
 * a field and calling it later throws "Illegal invocation" in a page while
 * working fine in Node — so Node-only tests cannot catch it. This suite
 * reproduces the browser rule with a receiver-checking stub.
 */
import { describe, it, expect, afterEach } from "vitest";

import { dohTxtResolver } from "../src/browser/index.js";
import { RelayClient } from "../src/http.js";
import { resolveIdentity } from "../src/resolve.js";

const realFetch = globalThis.fetch;

/**
 * Stand in for the browser's `fetch`, which is only callable with the global
 * as its receiver. A plain `const f = globalThis.fetch; f(url)` passes
 * `undefined`, which is exactly the case a page rejects with
 * "Illegal invocation" — so only `globalThis` is accepted here.
 */
function installReceiverCheckingFetch(respond: () => unknown) {
  const guarded = () => Promise.resolve(respond());
  Object.defineProperty(globalThis, "fetch", {
    configurable: true,
    writable: true,
    value: new Proxy(guarded, {
      apply(target, thisArg, args) {
        if (thisArg !== globalThis) {
          throw new TypeError("Failed to execute 'fetch' on 'Window': Illegal invocation");
        }
        return Reflect.apply(target, thisArg, args);
      },
    }),
  });
}

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  Object.defineProperty(globalThis, "fetch", {
    configurable: true,
    writable: true,
    value: realFetch,
  });
});

describe("default fetch is bound to its global", () => {
  it("RelayClient calls it with a legal receiver", async () => {
    installReceiverCheckingFetch(() => jsonResponse({ status: "ok" }));
    const client = new RelayClient("https://relay.example");
    await expect(client.request({ method: "GET", path: "/health" })).resolves.toEqual({
      status: "ok",
    });
  });

  it("dohTxtResolver calls it with a legal receiver", async () => {
    installReceiverCheckingFetch(() =>
      jsonResponse({ Status: 0, Answer: [{ data: '"poweur-pubkey=ed25519:AAA"' }] }),
    );
    await expect(dohTxtResolver().lookupTxt("_poweur.alice.example")).resolves.toEqual([
      "poweur-pubkey=ed25519:AAA",
    ]);
  });

  it("the well-known resolver calls it with a legal receiver", async () => {
    // resolveIdentity folds any web failure into "identity not found", so the
    // assertion is on *which* failure: a document rejected on its merits means
    // the fetch happened, an invocation error means it never did.
    installReceiverCheckingFetch(() => jsonResponse({ version: 1 }));
    const error = await resolveIdentity("alice.example", { skipDns: true }).catch((e) => e);
    expect(String(error)).not.toMatch(/Illegal invocation/);
    expect(String(error)).toMatch(/identity not found/);
  });
});
