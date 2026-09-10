/**
 * Conversation and thread grouping (EPIC-009 E09-T3).
 *
 * The tray's rows are what a user actually sees of typed messages and
 * threads, and they are pure functions over decrypted inbox records — so
 * they are tested directly rather than through the shell.
 */
import { describe, it, expect } from "vitest";

import { buildConversationRows, previewFor, threadLabel, threadsOf } from "../js/threads.js";

const ME = "alice.poweur.net";

const msg = (overrides) => ({
  id: "m",
  sender: "bob.example.org",
  recipient: ME,
  timestamp: "2026-01-15T09:00:00Z",
  plaintext: "hello",
  ...overrides,
});

describe("previewFor", () => {
  it("shows a chat.text as written, typed or not", () => {
    expect(previewFor(msg({ plaintext: "hi there" }))).toBe("hi there");
    expect(previewFor(msg({ type: "chat.text", plaintext: "hi there" }))).toBe("hi there");
  });

  it("falls back to a generic line for a type the chat UI cannot render", () => {
    expect(previewFor(msg({ type: "net.example.widget.poked", plaintext: '{"w":1}' }))).toBe(
      "app message from bob.example.org (net.example.widget.poked)",
    );
    expect(previewFor(msg({ type: "sys.sync.changed", plaintext: "{}" }))).toBe(
      "app message from bob.example.org (sys.sync.changed)",
    );
  });

  it("shows the padlock for anything we could not open, whatever its type", () => {
    expect(previewFor(msg({ plaintext: null }))).toBe("🔒 Could not decrypt");
    expect(previewFor(msg({ type: "net.example.widget.poked", plaintext: null }))).toBe(
      "🔒 Could not decrypt",
    );
  });
});

describe("threadsOf", () => {
  it("keeps an unthreaded conversation as a single default thread", () => {
    const groups = threadsOf([msg({ id: "m1" }), msg({ id: "m2" })]);
    expect(groups).toHaveLength(1);
    expect(groups[0].threaded).toBe(false);
    expect(groups[0].threadId).toBe("");
    expect(groups[0].messages.map((m) => m.id)).toEqual(["m1", "m2"]);
  });

  it("splits each thread_id into its own group, newest activity first", () => {
    const groups = threadsOf([
      msg({ id: "m1", thread_id: "thr_a", timestamp: "2026-01-15T09:00:00Z" }),
      msg({ id: "m2", timestamp: "2026-01-15T09:05:00Z" }),
      msg({ id: "m3", thread_id: "thr_a", timestamp: "2026-01-15T09:10:00Z" }),
    ]);
    expect(groups.map((g) => g.threadId)).toEqual(["thr_a", ""]);
    expect(groups[0].messages.map((m) => m.id)).toEqual(["m1", "m3"]);
    expect(groups[1].messages.map((m) => m.id)).toEqual(["m2"]);
  });
});

describe("buildConversationRows", () => {
  it("renders an unthreaded contact as exactly one row", () => {
    const rows = buildConversationRows(
      [msg({ id: "m1" }), msg({ id: "m2", plaintext: "second" })],
      ME,
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].contact).toBe("bob.example.org");
    expect(rows[0].threaded).toBe(false);
    expect(rows[0].preview).toBe("second");
  });

  it("finds the other side whether we sent or received", () => {
    const rows = buildConversationRows(
      [msg({ id: "m1", sender: ME, recipient: "carol.poweur.net", plaintext: "hi" })],
      ME,
    );
    expect(rows[0].contact).toBe("carol.poweur.net");
  });

  it("gives each thread its own row, sorted by last activity", () => {
    const rows = buildConversationRows(
      [
        msg({ id: "m1", timestamp: "2026-01-15T09:00:00Z", plaintext: "main" }),
        msg({ id: "m2", thread_id: "thr_x", timestamp: "2026-01-15T09:30:00Z", plaintext: "side" }),
      ],
      ME,
    );
    expect(rows.map((r) => r.threadId)).toEqual(["thr_x", ""]);
    expect(rows[0].preview).toBe("side");
    expect(rows[0].threaded).toBe(true);
    expect(rows[1].threaded).toBe(false);
  });

  it("shows a contact's unread badge once, on their newest row", () => {
    const rows = buildConversationRows(
      [
        msg({ id: "m1", timestamp: "2026-01-15T09:00:00Z" }),
        msg({ id: "m2", thread_id: "thr_x", timestamp: "2026-01-15T09:30:00Z" }),
      ],
      ME,
      () => 3,
    );
    expect(rows.map((r) => r.unread)).toEqual([3, 0]);
  });

  it("skips unsigned messages — they belong to the anonymous tray", () => {
    expect(buildConversationRows([msg({ id: "m1", sender: "" })], ME)).toHaveLength(0);
  });

  it("accepts records still held as JSON strings", () => {
    const rows = buildConversationRows([JSON.stringify(msg({ id: "m1" }))], ME);
    expect(rows).toHaveLength(1);
    expect(rows[0].lastMsg.id).toBe("m1");
  });

  it("uses the generic preview for an app message so no raw payload leaks in", () => {
    const rows = buildConversationRows(
      [msg({ id: "m1", type: "net.example.widget.poked", plaintext: '{"secret":"x"}' })],
      ME,
    );
    expect(rows[0].preview).toBe("app message from bob.example.org (net.example.widget.poked)");
    expect(rows[0].preview).not.toContain("secret");
  });
});

describe("threadLabel", () => {
  it("is empty for the default thread and truncates a long id", () => {
    expect(threadLabel("")).toBe("");
    expect(threadLabel("thr_x")).toBe("thr_x");
    expect(threadLabel("t".repeat(40))).toHaveLength(24);
    expect(threadLabel("t".repeat(40)).endsWith("…")).toBe(true);
  });
});
