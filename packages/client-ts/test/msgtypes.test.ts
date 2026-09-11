/**
 * Typed messages, threads, expiry and metadata (EPIC-009 E09-T3).
 *
 * `test/conformance.test.ts` pins the canonical string against Go's own
 * vectors; this file covers the rules around it — validation, the default
 * type, display fallback and thread grouping.
 */

import { describe, expect, it } from "vitest";

import { canonicalMessage } from "../src/canonical.js";
import {
  MAX_METADATA_KEYS,
  MAX_METADATA_KEY_LEN,
  MAX_METADATA_VAL_LEN,
  MAX_THREAD_ID_LEN,
  MSG_TYPE_CHAT_TEXT,
  SYSTEM_MESSAGE_TYPES,
  describeMessage,
  groupByThread,
  isKnownSystemType,
  isSystemType,
  metadataLines,
  normalizeMessageType,
  validateEnvelopeExtensions,
  validateExpiresAt,
  validateMessageType,
  validateMetadata,
  validateThreadId,
} from "../src/msgtypes.js";

describe("message types", () => {
  it("treats an absent type as chat.text without rewriting the envelope", () => {
    expect(normalizeMessageType(undefined)).toBe(MSG_TYPE_CHAT_TEXT);
    expect(normalizeMessageType("")).toBe(MSG_TYPE_CHAT_TEXT);
    expect(normalizeMessageType("  ")).toBe(MSG_TYPE_CHAT_TEXT);
    expect(normalizeMessageType("chat.text")).toBe(MSG_TYPE_CHAT_TEXT);
    expect(normalizeMessageType("net.example.thing")).toBe("net.example.thing");
  });

  it("accepts namespaced lowercase types", () => {
    for (const type of [
      "",
      "chat.text",
      "chat.attachment",
      "sys.contact.request",
      "net.poweur.tasks.assigned",
      "app.a-b.c9",
    ]) {
      expect(validateMessageType(type), type).toBeNull();
    }
  });

  it("rejects bare, uppercase, empty-segment and over-long types", () => {
    expect(validateMessageType("widget")).toContain("namespaced");
    expect(validateMessageType("chat.")).toContain("empty segment");
    expect(validateMessageType(".text")).toContain("empty segment");
    expect(validateMessageType("chat..text")).toContain("empty segment");
    expect(validateMessageType("Chat.Text")).toContain("invalid character");
    expect(validateMessageType("chat.te xt")).toContain("invalid character");
    expect(validateMessageType("chat.-text")).toContain("invalid character");
    expect(validateMessageType("chat.text-")).toContain("invalid character");
    expect(validateMessageType("a.b".repeat(40))).toContain("too long");
  });

  it("reserves the sys.* namespace, registered or not", () => {
    expect(isSystemType("sys.anything.at.all")).toBe(true);
    expect(isSystemType("chat.text")).toBe(false);
    expect(isKnownSystemType("sys.anything.at.all")).toBe(false);
    for (const known of SYSTEM_MESSAGE_TYPES) {
      expect(isSystemType(known), known).toBe(true);
      expect(isKnownSystemType(known), known).toBe(true);
      expect(validateMessageType(known), known).toBeNull();
    }
    // Sorted, so it can be shown to a user verbatim.
    expect([...SYSTEM_MESSAGE_TYPES]).toEqual([...SYSTEM_MESSAGE_TYPES].sort());
  });
});

describe("thread ids", () => {
  it("accepts opaque single-line identifiers", () => {
    for (const id of ["", "thr_01j9", "a.b:c@d+e~f-g", "t".repeat(MAX_THREAD_ID_LEN)]) {
      expect(validateThreadId(id), id).toBeNull();
    }
  });

  it("rejects anything that would make the canonical line ambiguous", () => {
    for (const id of ["has space", "new\nline", "tab\there", "slash/es"]) {
      expect(validateThreadId(id), id).toBeTruthy();
    }
    expect(validateThreadId("t".repeat(MAX_THREAD_ID_LEN + 1))).toContain("too long");
  });
});

describe("expires_at", () => {
  it("requires RFC3339, matching Go's parser rather than Date.parse", () => {
    expect(validateExpiresAt("")).toBeNull();
    expect(validateExpiresAt("2026-01-15T09:30:00Z")).toBeNull();
    expect(validateExpiresAt("2026-01-15T09:30:00.500Z")).toBeNull();
    expect(validateExpiresAt("2026-01-15T09:30:00+02:00")).toBeNull();
    // Date.parse accepts all of these; RFC3339 (and Go) does not.
    for (const bad of ["2026-01-15", "yesterday", "1768469400", "2026-01-15 09:30:00Z", "2026-01-15T09:30:00"]) {
      expect(validateExpiresAt(bad), bad).toBeTruthy();
    }
  });
});

describe("metadata", () => {
  it("accepts a small flat map of printable strings", () => {
    expect(validateMetadata(undefined)).toBeNull();
    expect(validateMetadata({})).toBeNull();
    expect(validateMetadata({ a: "", "mime.type": "image/png", "size-9": "1024" })).toBeNull();
  });

  it("rejects keys and values that would break the canonical line", () => {
    expect(validateMetadata({ "": "x" })).toContain("empty key");
    expect(validateMetadata({ Mime: "x" })).toContain("invalid character");
    expect(validateMetadata({ _mime: "x" })).toContain("invalid character");
    expect(validateMetadata({ ["k".repeat(MAX_METADATA_KEY_LEN + 1)]: "x" })).toContain("too long");
    expect(validateMetadata({ k: "v".repeat(MAX_METADATA_VAL_LEN + 1) })).toContain("too long");
    expect(validateMetadata({ k: "a\nb" })).toContain("control characters");
    expect(validateMetadata({ k: "a\tb" })).toContain("control characters");
    expect(validateMetadata({ k: "a\x7fb" })).toContain("control characters");
  });

  it("caps the number of keys and the total size", () => {
    const tooMany: Record<string, string> = {};
    for (let i = 0; i <= MAX_METADATA_KEYS; i++) tooMany[`${String.fromCharCode(97 + i)}key`] = "v";
    expect(validateMetadata(tooMany)).toContain("max");

    const tooBig: Record<string, string> = {};
    for (let i = 0; i < MAX_METADATA_KEYS; i++) {
      tooBig[`${String.fromCharCode(97 + i)}key`] = "v".repeat(MAX_METADATA_VAL_LEN);
    }
    expect(validateMetadata(tooBig)).toContain("too large");
  });

  it("renders one sorted, escape-free line per entry", () => {
    expect(metadataLines(undefined)).toEqual([]);
    expect(metadataLines({ zeta: "last", alpha: "first", "m.i-d_9": "middle" })).toEqual([
      "meta:alpha:first",
      "meta:m.i-d_9:middle",
      "meta:zeta:last",
    ]);
  });

  it("sorts independently of insertion order", () => {
    const forward = metadataLines({ a: "1", b: "2", c: "3" });
    const backward = metadataLines({ c: "3", b: "2", a: "1" });
    expect(forward).toEqual(backward);
  });
});

describe("canonicalMessage envelope extensions", () => {
  const base = {
    sender: "alice.poweur.net",
    recipient: "bob.example.org",
    timestamp: "2026-01-15T09:30:00Z",
    payload: "Y2lwaGVy",
    id: "msg_1",
    encryption: { alg: "x25519-chacha20-poly1305", ephemeral_public_key: "ZXBo", nonce: "bm9u" },
  };
  const head =
    "alice.poweur.net\nbob.example.org\n2026-01-15T09:30:00Z\nY2lwaGVy\nid:msg_1\nenc:x25519-chacha20-poly1305:ZXBo:bm9u";

  it("produces the pre-E09-T3 string when no new field is set", () => {
    expect(canonicalMessage(base)).toBe(head);
  });

  it("appends each field independently, never as a block", () => {
    expect(canonicalMessage({ ...base, threadId: "thr_1" })).toBe(`${head}\nthread:thr_1`);
    expect(canonicalMessage({ ...base, expiresAt: "2026-01-16T09:30:00Z" })).toBe(
      `${head}\nexpires:2026-01-16T09:30:00Z`,
    );
    expect(canonicalMessage({ ...base, metadata: { mime: "image/png" } })).toBe(
      `${head}\nmeta:mime:image/png`,
    );
  });

  it("orders type, thread, expires and sorted metadata after the existing lines", () => {
    expect(
      canonicalMessage({
        ...base,
        type: "chat.attachment",
        threadId: "thr_1",
        expiresAt: "2026-01-16T09:30:00Z",
        metadata: { zeta: "z", bytes: "20480", mime: "image/png" },
      }),
    ).toBe(
      `${head}\ntype:chat.attachment\nthread:thr_1\nexpires:2026-01-16T09:30:00Z` +
        "\nmeta:bytes:20480\nmeta:mime:image/png\nmeta:zeta:z",
    );
  });

  it("keeps session: between id: and enc: now that lines follow it", () => {
    expect(canonicalMessage({ ...base, sessionId: "sess_1", threadId: "thr_1" })).toBe(
      "alice.poweur.net\nbob.example.org\n2026-01-15T09:30:00Z\nY2lwaGVy" +
        "\nid:msg_1\nsession:sess_1\nenc:x25519-chacha20-poly1305:ZXBo:bm9u\nthread:thr_1",
    );
  });
});

describe("validateEnvelopeExtensions", () => {
  it("passes a bare and a fully populated envelope", () => {
    expect(validateEnvelopeExtensions({})).toBeNull();
    expect(
      validateEnvelopeExtensions({
        type: "chat.attachment",
        threadId: "thr_1",
        expiresAt: "2026-01-16T09:30:00Z",
        metadata: { mime: "image/png" },
      }),
    ).toBeNull();
  });

  it("reports the first failing field", () => {
    expect(validateEnvelopeExtensions({ type: "widget" })).toContain("namespaced");
    expect(validateEnvelopeExtensions({ threadId: "thr one" })).toContain("thread_id");
    expect(validateEnvelopeExtensions({ expiresAt: "nope" })).toContain("expires_at");
    expect(validateEnvelopeExtensions({ metadata: { K: "v" } })).toContain("metadata");
  });
});

describe("display fallback", () => {
  it("renders chat.text as written and everything else generically", () => {
    expect(describeMessage("carol.poweur.net", undefined, "hello")).toBe("hello");
    expect(describeMessage("carol.poweur.net", "chat.text", "hello")).toBe("hello");
    // A contact request's payload is prose written to be read.
    expect(describeMessage("carol.poweur.net", "sys.contact.request", "hi, it's carol")).toBe(
      "hi, it's carol",
    );
    expect(describeMessage("carol.poweur.net", "net.example.widget.poked", '{"w":1}')).toBe(
      "app message from carol.poweur.net (net.example.widget.poked)",
    );
    expect(describeMessage("carol.poweur.net", "sys.sync.changed", "{}")).toBe(
      "app message from carol.poweur.net (sys.sync.changed)",
    );
  });
});

describe("thread grouping", () => {
  it("keeps unthreaded messages as a flat list, in order", () => {
    const groups = groupByThread([{ id: "m1" }, { id: "m2" }, { id: "m3" }]);
    expect(groups.map((g) => g.threadId)).toEqual(["m1", "m2", "m3"]);
    expect(groups.every((g) => !g.threaded)).toBe(true);
  });

  it("collects a thread's messages together at its first appearance", () => {
    const groups = groupByThread([
      { id: "m1", thread_id: "t1" },
      { id: "m2" },
      { id: "m3", thread_id: "t1" },
      { id: "m4", thread_id: "t2" },
    ]);
    expect(groups.map((g) => g.threadId)).toEqual(["t1", "m2", "t2"]);
    expect(groups[0]?.messages.map((m) => m.id)).toEqual(["m1", "m3"]);
    expect(groups[0]?.threaded).toBe(true);
    expect(groups[1]?.threaded).toBe(false);
  });

  it("does not let a thread_id collide with a bare message id", () => {
    // A thread literally named "m2" must not swallow the standalone message
    // whose id is "m2" — the keys live in separate namespaces.
    const groups = groupByThread([{ id: "m1", thread_id: "m2" }, { id: "m2" }]);
    expect(groups).toHaveLength(2);
    expect(groups[0]?.messages.map((m) => m.id)).toEqual(["m1"]);
    expect(groups[1]?.messages.map((m) => m.id)).toEqual(["m2"]);
  });
});
