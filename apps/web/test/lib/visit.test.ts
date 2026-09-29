import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  chatTargetQuery,
  clearPendingChatTarget,
  forgetIdHint,
  pendingChatTarget,
  readIdHints,
  rememberIdHint,
  takeChatTarget,
  validChatTarget,
} from "../../src/lib/visit";

describe("chat target from an identity page", () => {
  beforeEach(() => {
    sessionStorage.clear();
    history.replaceState(null, "", "/app/");
  });

  it("accepts identity hosts only", () => {
    expect(validChatTarget(" Bob.Poweur.net. ")).toBe("bob.poweur.net");
    expect(validChatTarget("bob")).toBe("");
    expect(validChatTarget("javascript:alert(1)")).toBe("");
    expect(validChatTarget("bob.poweur.net/x")).toBe("");
    expect(validChatTarget("-bob.poweur.net")).toBe("");
  });

  it("takes ?to= out of the address bar and keeps it for the tab", () => {
    history.replaceState(null, "", "/app/?to=bob.poweur.net&handle=al#x");
    expect(takeChatTarget()).toBe("bob.poweur.net");
    expect(location.search).toBe("?handle=al");
    expect(location.hash).toBe("#x");
    expect(pendingChatTarget()).toBe("bob.poweur.net");
    expect(chatTargetQuery()).toBe("?to=bob.poweur.net");
    // A later boot on the same tab without the query keeps it.
    expect(takeChatTarget()).toBe("bob.poweur.net");
    clearPendingChatTarget();
    expect(chatTargetQuery()).toBe("");
  });

  it("drops an invalid target without keeping it", () => {
    history.replaceState(null, "", "/app/?to=%3Cscript%3E");
    expect(takeChatTarget()).toBe("");
    expect(location.search).toBe("");
    expect(pendingChatTarget()).toBe("");
  });
});

describe("ID hint cookie", () => {
  let jar = "";
  const written: string[] = [];

  beforeEach(() => {
    jar = "";
    written.length = 0;
    vi.spyOn(document, "cookie", "get").mockImplementation(() => jar);
    vi.spyOn(document, "cookie", "set").mockImplementation((value: string) => {
      written.push(value);
      const [pair] = value.split(";");
      jar = /Max-Age=0/.test(value) ? "" : pair;
    });
  });
  afterEach(() => vi.restoreAllMocks());

  it("reads the IDs, skipping anything that is not one", () => {
    expect(readIdHints("a=1; poweur_ids=alice.poweur.net%7Cbad%20id%7Cwork.poweur.net")).toEqual([
      "alice.poweur.net",
      "work.poweur.net",
    ]);
    expect(readIdHints("other=1")).toEqual([]);
  });

  it("remembers the newest first on the parent domain, and forgets", () => {
    rememberIdHint("alice.poweur.net", "poweur.net");
    rememberIdHint("work.poweur.net", "poweur.net");
    rememberIdHint("alice.poweur.net", "poweur.net");
    expect(readIdHints()).toEqual(["alice.poweur.net", "work.poweur.net"]);
    expect(written.at(-1)).toMatch(/; Domain=poweur\.net; Path=\/; SameSite=Lax/);

    forgetIdHint("alice.poweur.net", "poweur.net");
    expect(readIdHints()).toEqual(["work.poweur.net"]);
    forgetIdHint("work.poweur.net", "poweur.net");
    expect(written.at(-1)).toMatch(/Max-Age=0/);
  });

  it("does not write an ID outside the domain", () => {
    rememberIdHint("alice.example.com", "poweur.net");
    rememberIdHint("alice.poweur.net", undefined);
    expect(written).toEqual([]);
  });
});
