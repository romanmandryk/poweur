import { describe, expect, it } from "vitest";
import { signInCodeFromAppUrl, signInCodeFromInput } from "../../src/lib/app-link";

describe("signInCodeFromAppUrl", () => {
  it("returns the request code from a poweur://auth link", () => {
    expect(signInCodeFromAppUrl("poweur://auth?request=abc123")).toBe("abc123");
    expect(signInCodeFromAppUrl("poweur://auth?request=abc%2B123")).toBe("abc+123");
  });

  it("ignores other schemes, hosts, and links with no request", () => {
    expect(signInCodeFromAppUrl("https://alice.poweur.net/app/?auth=abc")).toBeNull();
    expect(signInCodeFromAppUrl("poweur://other?request=abc")).toBeNull();
    expect(signInCodeFromAppUrl("poweur://auth")).toBeNull();
    expect(signInCodeFromAppUrl("not a url")).toBeNull();
    expect(signInCodeFromAppUrl("")).toBeNull();
  });
});

describe("signInCodeFromInput", () => {
  it("strips a pasted link down to its code and leaves a code alone", () => {
    expect(signInCodeFromInput("  poweur://auth?request=abc123  ")).toBe("abc123");
    expect(signInCodeFromInput("abc123")).toBe("abc123");
  });
});

describe("requests by reference (E08-T6)", () => {
  it("hands the approve screen the short link from poweur://auth?request_uri=", () => {
    expect(signInCodeFromAppUrl("poweur://auth?request_uri=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2")).toBe(
      "https://oauth.poweur.org/r/K7QM4XP2",
    );
  });
});
