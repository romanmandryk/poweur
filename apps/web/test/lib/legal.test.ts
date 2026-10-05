import { describe, expect, it } from "vitest";
import { LEGAL_LINKS, showsPoweurLegal } from "../../src/lib/legal";

describe("legal links", () => {
  it("point at the website's legal pages", () => {
    expect(LEGAL_LINKS).toEqual({
      privacy: "https://poweur.org/legal/privacy/",
      terms: "https://poweur.org/legal/terms/",
      legal: "https://poweur.org/legal/",
    });
  });

  it("apply on poweur.net's relay, or to an ID under poweur.net", () => {
    expect(showsPoweurLegal(["poweur.net"])).toBe(true);
    expect(showsPoweurLegal(["example.org", "poweur.net"])).toBe(true);
    expect(showsPoweurLegal([], "alice.poweur.net")).toBe(true);
  });

  it("do not apply to a self-hosted relay, whose operator has their own terms", () => {
    expect(showsPoweurLegal(["example.org"])).toBe(false);
    expect(showsPoweurLegal(undefined, "carl.example.org")).toBe(false);
    expect(showsPoweurLegal([], "notpoweur.net")).toBe(false);
  });
});

describe("feedback", () => {
  it("goes to a Poweur ID on the hosted domain", async () => {
    const { FEEDBACK_ID } = await import("../../src/lib/legal");
    expect(FEEDBACK_ID).toBe("support.poweur.net");
  });
});
