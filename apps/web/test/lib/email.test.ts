// @vitest-environment jsdom
// jsdom, not happy-dom: DOMPurify 3.4 mis-sanitizes under happy-dom (keeps <style>,
// strips plain links), while real browsers and jsdom agree.
/**
 * The Email tray's logic (EPIC-023 E23-T6): what counts as mail, how it
 * threads, what a reply says, and that hostile HTML renders inert.
 */
import { describe, expect, it } from "vitest";
import type { EmailMessage } from "@poweur/client";

import {
  EMAIL_FRAME_SANDBOX, emailCSP, mailEntries, mailThreads, parseAddresses, prepareEmailHtml, replyDraft, NO_REPLY_AFTER_MS, sendStateLabel, sendStateOf,
} from "../../src/lib/email";

const ME = "john.poweur.net";
const BRIDGE = "email.poweur.net";

const email = (over: Partial<EmailMessage> = {}): EmailMessage => ({
  version: 1, from: { name: "Ann", address: "ann@example.com" }, to: [{ address: "john@poweur.net" }],
  subject: "Lunch", message_id: "m1@example.com", auth: { verdict: "verified" }, text: "Friday?", ...over,
});

const inbound = (id: string, ts: string, e: EmailMessage, sender = BRIDGE) => ({
  id, sender, recipient: ME, timestamp: ts, type: "email.message", plaintext: JSON.stringify(e),
});

describe("mailEntries", () => {
  it("takes mail only from the bridge the policy names", () => {
    const store = [
      inbound("a", "2026-10-13T09:00:00Z", email()),
      inbound("forged", "2026-10-13T09:01:00Z", email({ subject: "Your bank" }), "mallory.poweur.net"),
      { id: "c", sender: "bob.poweur.net", recipient: ME, timestamp: "2026-10-13T09:02:00Z", plaintext: "chat" },
    ];
    const entries = mailEntries(store, BRIDGE, ME);
    expect(entries.map((e) => e.id)).toEqual(["a"]);
    expect(mailEntries(store, "", ME)).toEqual([]);
  });

  it("attaches the newest status to a sent email", () => {
    const send = { version: 1, send_id: "S1", to: [{ address: "ann@example.com" }], subject: "Re: Lunch", text: "Yes", in_reply_to: "m1@example.com", references: ["m1@example.com"] };
    const store = [
      { id: "s", sender: ME, recipient: BRIDGE, timestamp: "2026-10-13T10:00:00Z", type: "email.send", plaintext: JSON.stringify(send) },
      { id: "st1", sender: BRIDGE, recipient: ME, timestamp: "2026-10-13T10:00:01Z", type: "email.status", plaintext: JSON.stringify({ version: 1, send_id: "S1", state: "accepted", at: "2026-10-13T10:00:01Z" }) },
      { id: "st2", sender: BRIDGE, recipient: ME, timestamp: "2026-10-13T10:00:02Z", type: "email.status", plaintext: JSON.stringify({ version: 1, send_id: "S1", state: "sent", message_id: "out1@poweur.net", at: "2026-10-13T10:00:02Z" }) },
    ];
    const [entry] = mailEntries(store, BRIDGE, ME);
    expect(entry.kind).toBe("sent");
    expect(entry.status?.state).toBe("sent");
    expect(entry.key).toBe("mail:m1@example.com");
  });
});

describe("sendStateOf", () => {
  const send = { version: 1, send_id: "S1", to: [{ address: "ann@example.com" }], subject: "Hi", text: "Yo" };
  const sentMsg = (id: string, ts: string) => ({ id, sender: ME, recipient: BRIDGE, timestamp: ts, type: "email.send", plaintext: JSON.stringify(send) });
  const at = Date.parse("2026-10-13T10:00:00Z");

  it("says Sending… until the bridge has been silent too long, then no_reply", () => {
    const [entry] = mailEntries([sentMsg("s", "2026-10-13T10:00:00Z")], BRIDGE, ME);
    expect(sendStateOf(entry, at + NO_REPLY_AFTER_MS - 1)).toBeUndefined();
    expect(sendStateOf(entry, at + NO_REPLY_AFTER_MS)).toBe("no_reply");
    expect(sendStateLabel("no_reply")).toBe("No reply from the email service");
  });

  it("trusts any status from the bridge, however old", () => {
    const store = [
      sentMsg("s", "2026-10-13T10:00:00Z"),
      { id: "st", sender: BRIDGE, recipient: ME, timestamp: "2026-10-13T10:00:01Z", type: "email.status", plaintext: JSON.stringify({ version: 1, send_id: "S1", state: "deferred", at: "2026-10-13T10:00:01Z" }) },
    ];
    const [entry] = mailEntries(store, BRIDGE, ME);
    expect(sendStateOf(entry, at + 10 * NO_REPLY_AFTER_MS)).toBe("deferred");
  });

  it("keeps only the newest copy of a send sent again", () => {
    const entries = mailEntries([sentMsg("old", "2026-10-13T10:00:00Z"), sentMsg("new", "2026-10-13T10:30:00Z")], BRIDGE, ME);
    expect(entries.map((e) => e.id)).toEqual(["new"]);
    expect(entries[0].key).toBe("mail:old");
    expect(sendStateOf(entries[0], Date.parse("2026-10-13T10:31:00Z"))).toBeUndefined();
  });
});

describe("mailThreads", () => {
  it("threads a reply with what it answers, even under another subject", () => {
    const store = [
      inbound("a", "2026-10-13T09:00:00Z", email()),
      inbound("b", "2026-10-13T11:00:00Z", email({ subject: "Change of plan", message_id: "m2@example.com", in_reply_to: "m1@example.com" })),
      inbound("c", "2026-10-13T12:00:00Z", email({ subject: "Other", message_id: "x@example.com", from: { address: "bob@example.com" } })),
    ];
    const threads = mailThreads(mailEntries(store, BRIDGE, ME), (e) => e.id !== "a");
    expect(threads.map((t) => t.entries.map((e) => e.id))).toEqual([["c"], ["a", "b"]]);
    expect(threads[1].subject).toBe("Change of plan");
    expect(threads[1].unread).toBe(1);
  });

  it("reports the weakest verdict and spam", () => {
    const store = [
      inbound("a", "2026-10-13T09:00:00Z", email()),
      inbound("b", "2026-10-13T10:00:00Z", email({ message_id: "m2@example.com", references: ["m1@example.com"], auth: { verdict: "failed", spam: "spam" } })),
    ];
    const [t] = mailThreads(mailEntries(store, BRIDGE, ME));
    expect(t.verdict).toBe("failed");
    expect(t.spam).toBe(true);
  });
});

describe("replyDraft", () => {
  const original = email({
    cc: [{ address: "carol@example.com" }, { address: "john@poweur.net" }], references: ["root@example.com"],
    date: "2026-10-13T09:00:00Z", text: "Friday?\nAt noon.",
  });
  it("replies to the sender, quoting, threaded", () => {
    const d = replyDraft(original, "reply", "john@poweur.net");
    expect(d.to).toBe("Ann <ann@example.com>");
    expect(d.cc).toBe("");
    expect(d.subject).toBe("Re: Lunch");
    expect(d.in_reply_to).toBe("m1@example.com");
    expect(d.references).toEqual(["root@example.com", "m1@example.com"]);
    expect(d.text).toContain("> Friday?\n> At noon.");
  });
  it("reply-all copies everyone but us", () => {
    expect(replyDraft(original, "all", "john@poweur.net").cc).toBe("carol@example.com");
  });
  it("forward starts empty and does not thread", () => {
    const d = replyDraft(email({ subject: "Re: Lunch" }), "forward", "john@poweur.net");
    expect(d.to).toBe("");
    expect(d.subject).toBe("Fwd: Lunch");
    expect(d.in_reply_to).toBeUndefined();
    expect(d.text).toContain("Forwarded message");
  });
  it("honours Reply-To", () => {
    expect(replyDraft(email({ reply_to: [{ address: "list@example.com" }] }), "reply", "john@poweur.net").to).toBe("list@example.com");
  });
});

describe("parseAddresses", () => {
  it("reads names, commas and refuses junk", () => {
    expect(parseAddresses("Ann <ann@example.com>, bob@example.com")).toEqual([{ name: "Ann", address: "ann@example.com" }, { address: "bob@example.com" }]);
    expect(() => parseAddresses("not an address")).toThrow();
    expect(parseAddresses("  ")).toEqual([]);
  });
});

describe("prepareEmailHtml: the hostile corpus renders inert", () => {
  const corpus: [string, string][] = [
    ["script", `<p>x</p><script>parent.steal()</script>`],
    ["event handler", `<img src="data:image/png;base64,iVBORw0KGgo=" onerror="parent.steal()"><div onclick="x()">x</div>`],
    ["javascript url", `<a href="javascript:parent.steal()">x</a>`],
    ["base", `<base href="https://evil.example/">`],
    ["meta refresh", `<meta http-equiv="refresh" content="0;url=https://evil.example">`],
    ["css import", `<style>@import url(https://t.example/a.css)</style>`],
    ["css url beacon", `<div style="background:url(https://t.example/p.gif)">x</div>`],
    ["svg", `<svg><script>steal()</script><image href="https://t.example/p"/></svg>`],
    ["form", `<form action="https://evil.example"><input name="password"><button>go</button></form>`],
    ["iframe", `<iframe src="https://evil.example"></iframe>`],
    ["object", `<object data="https://evil.example/x.swf"></object>`],
    ["srcset", `<img srcset="https://t.example/p.gif 1x">`],
    ["link", `<link rel="stylesheet" href="https://t.example/a.css">`],
  ];
  for (const [name, html] of corpus) {
    it(name, () => {
      const { srcdoc } = prepareEmailHtml(html);
      const body = srcdoc.slice(srcdoc.indexOf("<body>"));
      for (const bad of ["<script", "onerror", "onclick", "javascript:", "<base", "refresh", "@import", "url(", "<svg", "<form", "<input", "<iframe", "<object", "srcset", "<link", "https://"]) {
        expect(body.toLowerCase()).not.toContain(bad);
      }
    });
  }

  it("blocks remote images until asked, and only https then", () => {
    const html = `<img src="https://img.example/a.png"><img src="http://img.example/b.png">`;
    const blocked = prepareEmailHtml(html);
    expect(blocked.blockedImages).toBe(true);
    expect(blocked.srcdoc).not.toContain("img.example");
    expect(blocked.srcdoc).toContain(`img-src data: blob:;`);
    const loaded = prepareEmailHtml(html, [], { loadImages: true });
    expect(loaded.srcdoc).toContain("https://img.example/a.png");
    expect(loaded.srcdoc).not.toContain("http://img.example/b.png");
    expect(loaded.srcdoc).toContain("img-src data: blob: https:");
  });

  it("maps cid: images to their inline attachments", () => {
    const { srcdoc } = prepareEmailHtml(`<img src="cid:Logo@x">`, [{ name: "logo.png", mime: "image/png", size: 4, content_id: "logo@x", data: "iVBORw0KGgo=" }]);
    expect(srcdoc).toContain(`src="data:image/png;base64,iVBORw0KGgo="`);
  });

  it("opens links in a new tab without a referrer", () => {
    const { srcdoc } = prepareEmailHtml(`<a href="https://example.com/x">x</a>`);
    expect(srcdoc).toMatch(/<a href="https:\/\/example.com\/x" target="_blank" rel="noopener noreferrer">/);
  });

  it("frames it with a CSP and a sandbox that allows no scripts", () => {
    expect(prepareEmailHtml("<p>x</p>").srcdoc).toContain(`content="${emailCSP(false)}"`);
    expect(emailCSP(false)).toContain("default-src 'none'");
    expect(EMAIL_FRAME_SANDBOX).not.toContain("allow-scripts");
    expect(EMAIL_FRAME_SANDBOX).not.toContain("allow-forms");
    expect(EMAIL_FRAME_SANDBOX).not.toContain("allow-top-navigation");
  });
});
