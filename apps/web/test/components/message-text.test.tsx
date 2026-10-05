import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { MessageText } from "../../src/components/MessageText";

const draw = (text: string | null) => render(<p>{<MessageText text={text} />}</p>).container.firstElementChild!;

describe("MessageText", () => {
  it("leaves ordinary text alone, emoji a person typed included", () => {
    const shown = draw("hello 📎 there");
    expect(shown.textContent).toBe("hello 📎 there");
    expect(shown.querySelector("svg")).toBeNull();
  });

  it("draws the attachment and undecryptable markers from lib/threads.js as icons", () => {
    const attachment = draw("📎 a.pdf");
    expect(attachment.textContent).toBe("a.pdf");
    expect(attachment.querySelector("svg.lucide-paperclip")).toBeTruthy();

    const sealed = draw("🔒 Could not decrypt");
    expect(sealed.textContent).toBe("Could not decrypt");
    expect(sealed.querySelector("svg.lucide-lock")).toBeTruthy();
  });

  it("draws a disappearing message's countdown with an hourglass", () => {
    const shown = draw("soon gone · ⏳ 5m");
    expect(shown.textContent).toBe("soon gone · 5m");
    expect(shown.querySelectorAll("svg")).toHaveLength(1);
    expect(shown.querySelector("svg.lucide-hourglass")).toBeTruthy();
  });

  it("renders nothing for no text", () => {
    expect(draw(null).textContent).toBe("");
  });

  describe("links", () => {
    const drawLinks = (text: string) => render(<p><MessageText text={text} links /></p>).container.firstElementChild!;

    it("makes http(s) URLs links that open in a new tab", () => {
      const shown = drawLinks("see https://poweur.net/docs and http://example.com/a?b=1#c ok");
      const links = [...shown.querySelectorAll("a")];
      expect(links.map((a) => a.getAttribute("href"))).toEqual(["https://poweur.net/docs", "http://example.com/a?b=1#c"]);
      for (const a of links) {
        expect(a.getAttribute("target")).toBe("_blank");
        expect(a.getAttribute("rel")).toContain("noopener");
      }
      expect(shown.textContent).toBe("see https://poweur.net/docs and http://example.com/a?b=1#c ok");
    });

    it("leaves sentence punctuation and a wrapping bracket outside the link", () => {
      const hrefs = (t: string) => [...drawLinks(t).querySelectorAll("a")].map((a) => a.getAttribute("href"));
      expect(hrefs("Visit https://poweur.net.")).toEqual(["https://poweur.net"]);
      expect(hrefs("(https://poweur.net/x)")).toEqual(["https://poweur.net/x"]);
      expect(hrefs("https://en.wikipedia.org/wiki/Foo_(bar)")).toEqual(["https://en.wikipedia.org/wiki/Foo_(bar)"]);
      expect(hrefs("really?! https://poweur.net/?a=1,")).toEqual(["https://poweur.net/?a=1"]);
    });

    it("does not link other schemes, and links nothing unless asked", () => {
      expect(drawLinks("javascript:alert(1) data:text/html,x ftp://x.y").querySelector("a")).toBeNull();
      expect(draw("https://poweur.net").querySelector("a")).toBeNull();
    });

    it("keeps the attachment icon and countdown around a link", () => {
      const shown = drawLinks("📎 https://poweur.net · ⏳ 5m");
      expect(shown.querySelector("a")).toBeTruthy();
      expect(shown.querySelector("svg.lucide-paperclip")).toBeTruthy();
      expect(shown.querySelector("svg.lucide-hourglass")).toBeTruthy();
    });
  });
});
