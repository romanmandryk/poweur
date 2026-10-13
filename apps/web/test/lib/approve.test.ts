import { describe, expect, it } from "vitest";
import { androidIntentFor, appLinkFor, approveDevice, approvePageLink } from "../../src/lib/approve";

const LINK = "https://oauth.poweur.org/r/K7QM4XP2";

describe("approve page (EPIC-028)", () => {
  it("tells phones from desktops", () => {
    expect(approveDevice("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)", 5)).toBe("ios");
    expect(approveDevice("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 5)).toBe("ios"); // iPadOS
    expect(approveDevice("Mozilla/5.0 (Linux; Android 15; Pixel 9)", 5)).toBe("android");
    expect(approveDevice("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 0)).toBe("desktop");
  });

  it("builds this page's link for a phone to scan", () => {
    const loc = { origin: "https://alice.poweur.net", pathname: "/id/approve" };
    expect(approvePageLink(LINK, loc)).toBe("https://alice.poweur.net/id/approve#auth=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2");
    expect(approvePageLink(LINK, { ...loc, pathname: "/id/" })).toBe("https://alice.poweur.net/id/approve#auth=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2");
  });

  it("hands a request to the app on Android, falling back to this page marked noapp", () => {
    const fallback = "https://alice.poweur.net/id/approve#auth=x";
    const intent = androidIntentFor(LINK, fallback);
    expect(intent.startsWith("intent://auth?request_uri=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2#Intent;")).toBe(true);
    expect(intent).toContain(";scheme=poweur;package=net.poweur.app;");
    expect(decodeURIComponent(intent.match(/S\.browser_fallback_url=([^;]+)/)![1])).toBe(`${fallback}&noapp`);
    expect(intent.endsWith(";end")).toBe(true);
  });

  it("gives the app link for a request link, and nothing for anything else", () => {
    expect(appLinkFor(LINK)).toBe("poweur://auth?request_uri=https%3A%2F%2Foauth.poweur.org%2Fr%2FK7QM4XP2");
    expect(appLinkFor("eyJ4IjoxfQ")).toBe("");
  });
});
