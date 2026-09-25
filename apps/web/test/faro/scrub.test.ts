import { describe, expect, it } from "vitest";
import { scrubItem, scrubText, scrubValue } from "@poweur/faro";

describe("scrubText without consent", () => {
  it("redacts Poweur IDs and other domains wherever they appear", () => {
    expect(scrubText("Could not restore alice.poweur.net: offline")).toBe("Could not restore <id>: offline");
    expect(scrubText("No Poweur ID named carl.example.com")).toBe("No Poweur ID named <id>");
    expect(scrubText("GET /dav/bob.poweur.net/private/notes.txt 403")).toBe("GET /dav/<id>/private/notes.txt 403");
    expect(scrubText("mail roman@example.pt")).toBe("mail <email>");
  });

  it("keeps the service's own hosts and file names, so stack traces stay useful", () => {
    expect(scrubText("at f (https://alice.poweur.net/app/assets/index-B41p5tEz.js:12:7)")).toBe(
      "at f (https://<id>/app/assets/index-B41p5tEz.js:12:7)",
    );
    expect(scrubText("https://poweur.net/app/assets/index.js")).toBe("https://poweur.net/app/assets/index.js");
    expect(scrubText("fetch https://oauth.poweur.org/token failed")).toBe("fetch https://oauth.poweur.org/token failed");
  });

  it("leaves ordinary code and numbers alone", () => {
    for (const text of ["e.target.value is undefined", "t.map is not a function", "version 0.1.37", "window.location.href"]) {
      expect(scrubText(text), text).toBe(text);
    }
  });
});

describe("secrets always go, even with consent", () => {
  it("drops URL queries and fragments, where claim and pairing links carry keys", () => {
    const claim = "https://alice.poweur.net/app/#claim=eyJpZGVudGl0eSI6ImFsaWNlIn0";
    expect(scrubText(claim, true)).toBe("https://alice.poweur.net/app/");
    expect(scrubText(claim)).toBe("https://<id>/app/");
    expect(scrubText("open https://poweur.net/app/?handle=alice&from=signin now", true)).toBe("open https://poweur.net/app/ now");
    expect(scrubText("pairing #pair=abc123 started", true)).toBe("pairing  started");
  });

  it("keeps IDs when the user opted in", () => {
    expect(scrubText("Could not restore alice.poweur.net", true)).toBe("Could not restore alice.poweur.net");
  });
});

describe("scrubItem", () => {
  const item = {
    type: "exception",
    payload: { type: "Error", value: "send to bob.poweur.net failed", stacktrace: { frames: [{ filename: "https://alice.poweur.net/app/x.js" }] } },
    meta: {
      user: { id: "alice.poweur.net" },
      page: { url: "https://alice.poweur.net/app/#claim=secret" },
      app: { name: "poweur-web" },
      browser: { name: "Chrome", version: "152", os: "Mac OS", userAgent: "Mozilla/5.0 (Macintosh) Chrome/152" },
    },
  };

  it("anonymous: no user, no IDs, no secrets, no full user-agent", () => {
    const out = scrubItem(item);
    expect(out.meta?.user).toBeUndefined();
    expect(out.meta?.browser).toEqual({ name: "Chrome", version: "152", os: "Mac OS" });
    expect(JSON.stringify(out)).not.toMatch(/alice|bob|secret/);
    expect((out.payload as any).value).toBe("send to <id> failed");
  });

  it("identified: keeps the user and IDs, still drops secrets", () => {
    const out = scrubItem(item, true);
    expect(out.meta?.user).toEqual({ id: "alice.poweur.net" });
    expect(JSON.stringify(out)).not.toContain("secret");
  });

  it("does not modify the original", () => {
    scrubValue(item);
    expect(item.meta.user.id).toBe("alice.poweur.net");
  });
});
