import { describe, expect, it } from "vitest";
import { isRetryableSendError, loadWebOutbox, queueWebMessage, retryWebOutbox } from "../../src/lib/outbox.js";

function memoryStorage() {
  const values = new Map();
  return { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) };
}

describe("browser outbox", () => {
  it("persists offline messages and removes them after retry", async () => {
    const storage = memoryStorage();
    const sealed = { payload: "ciphertext", encryption: { alg: "test", nonce: "n", ephemeral_public_key: "k" } };
    queueWebMessage("alice.example", "bob.example", sealed, {}, "offline", storage);
    expect(loadWebOutbox("alice.example", storage)).toHaveLength(1);
    const delivered = [];
    const result = await retryWebOutbox("alice.example", async entry => delivered.push(entry.payload), { force: true, storage });
    expect(result).toEqual({ sent: 1, pending: 0 });
    expect(delivered).toEqual(["ciphertext"]);
    expect(JSON.stringify(loadWebOutbox("alice.example", storage))).not.toContain("hello");
  });

  it("keeps transport failures but drops permanent relay refusals", async () => {
    expect(isRetryableSendError(new TypeError("offline"))).toBe(true);
    expect(isRetryableSendError({ status: 503 })).toBe(true);
    expect(isRetryableSendError({ status: 403 })).toBe(false);
    expect(isRetryableSendError(new Error("invalid recipient"))).toBe(false);
  });
});
