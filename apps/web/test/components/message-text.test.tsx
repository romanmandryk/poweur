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
});
