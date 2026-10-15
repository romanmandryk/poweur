/**
 * Subjects in the tray and the composer (EPIC-009 E09-T13): both views'
 * rows from one fixture inbox, and every row of the composer table.
 */
import { describe, expect, it } from "vitest";

import {
  buildConversationRows, composerSubject, isBridgeMail, messageThreadKey, sendThread, subjectDivider,
  threadMessages, threadSubjects, threadTitle, unreadTotal,
} from "../../src/lib/threads.js";

const ME = "alice.poweur.net";
const BOB = "bob.poweur.net";
const TEAM = "team.acme.poweur.net";

const m = (id, minute, extra = {}) => ({
  id, sender: BOB, recipient: ME, timestamp: `2026-02-01T09:${String(minute).padStart(2, "0")}:00Z`, plaintext: `text ${id}`, ...extra,
});

// Two subject threads, a named thread, a plain stream, a group, a
// subject-without-thread (as the email bridge would), and an old client's
// reply that dropped the subject but kept the thread.
const INBOX = [
  m("a1", 0, { thread_id: "01JA", subject: "Q3 budget" }),
  m("p1", 1),
  m("o1", 2, { thread_id: "01JB", subject: "Offsite" }),
  m("a2", 3, { thread_id: "01JA" }), // old client: no subject, same thread
  m("n1", 4, { thread_id: "release-notes" }),
  m("s1", 5, { subject: "Re: Mail subject" }),
  m("g1", 6, { thread_id: TEAM, metadata: { group: TEAM, epoch: "1" }, group_verified: true, sender: "carol.poweur.net" }),
  m("g2", 7, { thread_id: `${TEAM}:01JC`, subject: "Plan", metadata: { group: TEAM, epoch: "1" }, group_verified: true, sender: "carol.poweur.net" }),
  m("p2", 8, { sender: ME, recipient: BOB }),
];

describe("By subject rows", () => {
  const rows = buildConversationRows(INBOX, ME, () => 0, { view: "subject", threadUnread: (peer, key) => (key === "01JB" ? 1 : 0) });
  const byKey = Object.fromEntries(rows.map((r) => [`${r.contact}|${r.threadId}`, r]));

  it("has one row per (peer, thread), newest first", () => {
    expect(rows.map((r) => `${r.contact}|${r.threadId}`)).toEqual([
      `${BOB}|`, `${TEAM}|${TEAM}:01JC`, `${TEAM}|${TEAM}`, `${BOB}|subj:mail subject`, `${BOB}|release-notes`, `${BOB}|01JA`, `${BOB}|01JB`,
    ]);
  });

  it("labels rows by subject, (no subject) and #name", () => {
    expect(byKey[`${BOB}|01JA`].title).toBe("Q3 budget");
    expect(byKey[`${BOB}|01JA`].messages.map((x) => x.id)).toEqual(["a1", "a2"]);
    expect(byKey[`${BOB}|`].title).toBe("(no subject)");
    expect(byKey[`${BOB}|release-notes`].title).toBe("#release-notes");
    expect(byKey[`${BOB}|subj:mail subject`].title).toBe("Re: Mail subject");
    expect(byKey[`${TEAM}|${TEAM}`].title).toBe("(no subject)");
    expect(byKey[`${TEAM}|${TEAM}:01JC`].title).toBe("Plan");
    expect(byKey[`${TEAM}|${TEAM}`].group).toBe(true);
  });

  it("counts unread per thread", () => {
    expect(byKey[`${BOB}|01JB`].unread).toBe(1);
    expect(byKey[`${BOB}|01JA`].unread).toBe(0);
  });
});

describe("By ID rows and the stream", () => {
  it("is one row per contact and group with a subject preview", () => {
    const rows = buildConversationRows(INBOX, ME);
    expect(rows.map((r) => r.contact)).toEqual([BOB, TEAM]);
    const group = buildConversationRows([INBOX[7]], ME)[0];
    expect(group.preview).toBe("Plan — text g2");
  });

  it("streams every thread and draws a divider where the thread changes", () => {
    const stream = threadMessages(INBOX, ME, BOB, null);
    expect(stream.map((x) => x.id)).toEqual(["a1", "p1", "o1", "a2", "n1", "s1", "p2"]);
    const subjects = threadSubjects(stream);
    const dividers = stream.map((x, i) => subjectDivider(x, stream[i - 1], "", subjects));
    // The old client's reply (a2) is labelled by its thread's subject.
    expect(dividers).toEqual(["Q3 budget", "(no subject)", "Offsite", "Q3 budget", "#release-notes", "Re: Mail subject", "(no subject)"]);
  });

  it("opens one thread by key", () => {
    expect(threadMessages(INBOX, ME, BOB, "01JA").map((x) => x.id)).toEqual(["a1", "a2"]);
    expect(threadMessages(INBOX, ME, BOB, "").map((x) => x.id)).toEqual(["p1", "p2"]);
  });

  it("keys a message by thread, then subject", () => {
    expect(messageThreadKey(m("x", 0, { thread_id: "T", subject: "S" }))).toBe("T");
    expect(messageThreadKey(m("x", 0, { subject: "Fwd: S" }))).toBe("subj:s");
    expect(threadTitle("", "")).toBe("(no subject)");
  });
});

describe("unread with thread marks", () => {
  it("reading one subject leaves the other unread", () => {
    const threads = { [BOB]: { "01JA": { timestamp: "2026-02-01T09:03:00Z", id: "a2" } } };
    const unread = unreadTotal(INBOX.filter((x) => x.sender === BOB), ME, {}, { threads });
    // a1 and a2 are covered by the 01JA mark; p1, o1, n1, s1 are not.
    expect(unread).toBe(4);
  });
});

describe("mail stays out of chat", () => {
  const mail = { id: "e1", sender: "email.poweur.net", recipient: ME, type: "email.message", timestamp: "2026-02-01T10:00:00Z", plaintext: "{}" };
  it("drops the trusted bridge's mail from rows and counts, not a stranger's", () => {
    expect(isBridgeMail(mail, "email.poweur.net")).toBe(true);
    expect(buildConversationRows([mail], ME, () => 0, { emailBridge: "email.poweur.net" })).toEqual([]);
    expect(unreadTotal([mail], ME, {}, { emailBridge: "email.poweur.net" })).toBe(0);
    const forged = { ...mail, sender: "mallory.poweur.net" };
    expect(buildConversationRows([forged], ME, () => 0, { emailBridge: "email.poweur.net" })).toHaveLength(1);
  });
});

describe("composerSubject (the E09-T13 table)", () => {
  const withSubject = [m("a1", 0, { thread_id: "01JA", subject: "Q3 budget" })];
  const plain = [m("p1", 1, { thread_id: "" })];

  it("new message from By ID: collapsed, default thread", () => {
    expect(composerSubject({ view: "id", origin: "new" })).toMatchObject({ expanded: false, value: "", threadId: "" });
  });
  it("new conversation from By subject: expanded", () => {
    expect(composerSubject({ view: "subject", origin: "new" })).toMatchObject({ expanded: true, value: "" });
  });
  it("reply when the newest message has a subject (either view): expanded, prefilled, its thread", () => {
    for (const view of ["id", "subject"]) {
      expect(composerSubject({ view, origin: "reply", conversation: withSubject, threadKey: view === "id" ? null : "01JA" }))
        .toMatchObject({ expanded: true, value: "Q3 budget", threadId: "01JA", startsNew: false });
    }
  });
  it("reply in By ID when the newest has no subject: collapsed, its thread", () => {
    expect(composerSubject({ view: "id", origin: "reply", conversation: [...withSubject, m("x", 5, { thread_id: "01JA" })] }))
      .toMatchObject({ expanded: false, value: "", threadId: "01JA" });
    expect(composerSubject({ view: "id", origin: "reply", conversation: plain })).toMatchObject({ expanded: false, threadId: "" });
  });
  it("reply in By subject inside a thread: the thread's subject, empty for (no subject)", () => {
    expect(composerSubject({ view: "subject", origin: "reply", conversation: plain, threadKey: "" }))
      .toMatchObject({ expanded: true, value: "", threadId: "" });
    expect(composerSubject({ view: "subject", origin: "reply", conversation: [m("g", 0, { thread_id: TEAM })], threadKey: TEAM, group: TEAM }))
      .toMatchObject({ threadId: TEAM, value: "" });
  });
  it("our own newest message counts", () => {
    const mine = [...plain, m("me", 9, { sender: ME, recipient: BOB, thread_id: "01JZ", subject: "Mine" })];
    expect(composerSubject({ view: "id", origin: "reply", conversation: mine })).toMatchObject({ value: "Mine", threadId: "01JZ" });
  });
  it("editing or clearing a prefilled subject warns; touching stops prefill", () => {
    expect(composerSubject({ view: "id", origin: "reply", conversation: withSubject, touched: true, typed: "Q4 budget" }).startsNew).toBe(true);
    expect(composerSubject({ view: "id", origin: "reply", conversation: withSubject, touched: true, typed: "" }).startsNew).toBe(true);
    expect(composerSubject({ view: "id", origin: "reply", conversation: withSubject, touched: true, typed: "re: q3  budget" }).startsNew).toBe(false);
    // A newer message arrives: untouched follows it, touched keeps the user's text.
    const newer = [...withSubject, m("o", 9, { thread_id: "01JB", subject: "Offsite" })];
    expect(composerSubject({ view: "id", origin: "reply", conversation: newer }).value).toBe("Offsite");
    expect(composerSubject({ view: "id", origin: "reply", conversation: newer, touched: true, typed: "Mine" }).value).toBe("Mine");
  });
});

describe("sendThread", () => {
  const mint = () => "01NEW";
  it("keeps, mints or clears the thread", () => {
    expect(sendThread({ subject: "Q3 budget", prefill: "Q3 budget", threadId: "01JA", mint })).toEqual({ subject: "Q3 budget", threadId: "01JA" });
    expect(sendThread({ subject: "Q4", prefill: "Q3 budget", threadId: "01JA", mint })).toEqual({ subject: "Q4", threadId: "01NEW" });
    expect(sendThread({ subject: "", prefill: "Q3 budget", threadId: "01JA", mint })).toEqual({ subject: "", threadId: "" });
    expect(sendThread({ subject: "Plan", prefill: "", threadId: TEAM, group: TEAM, mint })).toEqual({ subject: "Plan", threadId: `${TEAM}:01NEW` });
    expect(sendThread({ subject: "", prefill: "Plan", threadId: `${TEAM}:01JC`, group: TEAM, mint })).toEqual({ subject: "", threadId: TEAM });
    expect(sendThread({ subject: "", prefill: "", threadId: "01JA", mint })).toEqual({ subject: "", threadId: "01JA" });
  });
});
