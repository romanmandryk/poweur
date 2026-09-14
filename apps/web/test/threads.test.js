/**
 * Conversation and thread grouping (EPIC-009 E09-T3).
 *
 * The tray's rows are what a user actually sees of typed messages and
 * threads, and they are pure functions over decrypted inbox records — so
 * they are tested directly rather than through the shell.
 */
import { describe, it, expect } from "vitest";

import {
  bodyFor, buildConversationRows, conversationPeer, deliveryState, latestWindow, previewFor,
  threadLabel, threadMessages, threadsOf, THREAD_PAGE_SIZE,
} from "../js/threads.js";

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

  it("files verified fan-outs under the group rather than the speaking member", () => {
    const rows = buildConversationRows([msg({
      id: "m1",
      thread_id: "crew.example.org",
      metadata: { group: "crew.example.org", epoch: "3" },
      group_verified: true,
    })], ME);
    expect(rows[0].contact).toBe("crew.example.org");
    expect(rows[0].group).toBe(true);
  });

  it("does not trust an unverified group label", () => {
    const rows = buildConversationRows([msg({
      id: "m1",
      thread_id: "crew.example.org",
      metadata: { group: "crew.example.org", epoch: "3" },
    })], ME);
    expect(rows[0].contact).toBe("bob.example.org");
    expect(rows[0].group).toBe(false);
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

  it("keeps contact request and accept out of the chat list", () => {
    const rows = buildConversationRows(
      [
        msg({ id: "m1", type: "sys.contact.request", plaintext: "hi, it's bob" }),
        msg({ id: "m2", type: "sys.contact.accept", plaintext: "contact request accepted" }),
        msg({ id: "m3", plaintext: "actual chat" }),
      ],
      ME,
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].preview).toBe("actual chat");
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

describe("conversationPeer", () => {
  it("is the other side of a direct message, and the group of a verified fan-out", () => {
    expect(conversationPeer(msg({}), ME)).toBe("bob.example.org");
    expect(conversationPeer(msg({ sender: ME, recipient: "bob.example.org" }), ME)).toBe("bob.example.org");
    expect(conversationPeer(msg({ group_verified: true, metadata: { group: "crew.poweur.net" } }), ME))
      .toBe("crew.poweur.net");
    expect(conversationPeer(msg({ metadata: { group: "crew.poweur.net" } }), ME)).toBe("bob.example.org");
  });
});

describe("threadMessages", () => {
  const store = [
    msg({ id: "m1", timestamp: "2026-01-15T09:00:00Z" }),
    msg({ id: "m2", sender: ME, recipient: "bob.example.org", timestamp: "2026-01-15T09:01:00Z" }),
    msg({ id: "m3", thread_id: "thr_a", timestamp: "2026-01-15T09:02:00Z" }),
    msg({ id: "m4", sender: "carol.example.org", timestamp: "2026-01-15T09:03:00Z" }),
    JSON.stringify(msg({ id: "m5", timestamp: "2026-01-15T09:04:00Z" })),
    msg({ id: "anon", sender: "", timestamp: "2026-01-15T09:05:00Z" }),
  ];

  it("holds both sides of the default thread, oldest first, and nobody else", () => {
    expect(threadMessages(store, ME, "bob.example.org").map((m) => m.id)).toEqual(["m1", "m2", "m5"]);
  });

  it("keeps a named thread apart from the default one", () => {
    expect(threadMessages(store, ME, "bob.example.org", "thr_a").map((m) => m.id)).toEqual(["m3"]);
  });

  it("matches the peer case-insensitively", () => {
    expect(threadMessages(store, ME, "Bob.Example.org").map((m) => m.id)).toEqual(["m1", "m2", "m5"]);
  });
});

describe("latestWindow", () => {
  const list = Array.from({ length: 23 }, (_, i) => i + 1);

  it("shows the newest page and says more exist", () => {
    const page = latestWindow(list, THREAD_PAGE_SIZE);
    expect(page.visible).toEqual([14, 15, 16, 17, 18, 19, 20, 21, 22, 23]);
    expect(page.hidden).toBe(13);
    expect(page.hasMore).toBe(true);
  });

  it("has nothing more once everything is shown, however far past the end", () => {
    expect(latestWindow(list, 30)).toEqual({ visible: list, hidden: 0, hasMore: false });
    expect(latestWindow(list.slice(0, 4), THREAD_PAGE_SIZE).hasMore).toBe(false);
    expect(latestWindow([], THREAD_PAGE_SIZE)).toEqual({ visible: [], hidden: 0, hasMore: false });
  });
});

describe("deliveryState", () => {
  const sent = msg({ id: "out1", sender: ME, recipient: "bob.example.org" });

  it("is sent until an ack names the message", () => {
    expect(deliveryState(sent, [])).toBe("sent");
    expect(deliveryState(sent, [{ message_id: "other", state: "read" }])).toBe("sent");
  });

  it("ranks read above delivered, in whatever order the acks arrived", () => {
    expect(deliveryState(sent, [{ message_id: "out1", state: "delivered_client" }])).toBe("delivered");
    expect(deliveryState(sent, [
      { message_id: "out1", state: "read" },
      { message_id: "out1", state: "delivered_client" },
    ])).toBe("read");
  });

  it("reports the relay giving up over any delivery claim", () => {
    expect(deliveryState(sent, [
      { message_id: "out1", state: "delivered_client" },
      { message_id: "out1", type: "sys.delivery.failed" },
    ])).toBe("failed");
  });

  it("never matches a message without an id", () => {
    expect(deliveryState(msg({ id: undefined }), [{ message_id: undefined, state: "read" }])).toBe("sent");
  });
});

describe("bodyFor", () => {
  it("is the preview without the expiry suffix", () => {
    const expiring = msg({ plaintext: "soon gone", expires_at: new Date(Date.now() + 3_600_000).toISOString() });
    expect(bodyFor(expiring)).toBe("soon gone");
    expect(previewFor(expiring)).toMatch(/^soon gone · ⏳ /);
    expect(bodyFor(msg({ type: "chat.attachment", metadata: { attachment_name: "a.pdf" } }))).toBe("📎 a.pdf");
    expect(bodyFor(msg({ plaintext: null }))).toBe("🔒 Could not decrypt");
  });
});
