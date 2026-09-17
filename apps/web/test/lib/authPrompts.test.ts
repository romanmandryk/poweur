import { describe, expect, it } from "vitest";
import { pendingAuthPrompts } from "../../src/lib/authPrompts";
import { promptMessage } from "../helpers/prompts";

const now = Date.parse("2026-09-17T12:00:00Z");

describe("pendingAuthPrompts", () => {
  it("keeps live prompts and drops the rest", () => {
    const prompts = pendingAuthPrompts([
      promptMessage("a"),
      promptMessage("b", "2026-09-17T12:04:00Z"),
      promptMessage("dismissed"),
      promptMessage("expired", "2026-09-17T11:59:00Z"),
      promptMessage("garbled", undefined, { plaintext: "{not json" }),
      promptMessage("unsigned", undefined, { sender: "" }),
      { id: "chat", sender: "bob.poweur.net", type: "", plaintext: "hi" },
    ], ["dismissed"], now);
    expect(prompts.map((p) => p.id)).toEqual(["b", "a"]);
    expect(prompts[0]).toMatchObject({
      from: "bridge.poweur.org", client: "Team dashboard", clientHost: "grafana.example.org", audience: "https://oauth.poweur.org",
    });
  });
});
