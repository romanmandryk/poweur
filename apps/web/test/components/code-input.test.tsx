import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@testing-library/react";
import { CodeInput, codeChars } from "../../src/ui/CodeInput";

function setup() {
  const onComplete = vi.fn();
  const onLink = vi.fn();
  const view = render(<CodeInput id="code" label="Pairing code, 8 characters" onComplete={onComplete} onLink={onLink} />);
  const input = view.container.querySelector<HTMLInputElement>("#code")!;
  const cells = () => [...view.container.querySelectorAll(".code-cell")].map((c) => c.textContent);
  const shakes = () => view.container.querySelectorAll(".animate-shake").length;
  input.focus();
  return { input, cells, shakes, onComplete, onLink };
}

const paste = (el: HTMLElement, text: string) => fireEvent.paste(el, { clipboardData: { getData: () => text } });

describe("CodeInput (EPIC-028 E28-T9)", () => {
  it("reads codes the way Crockford base32 does", () => {
    expect(codeChars("k7qm-4xp2")).toBe("K7QM4XP2");
    expect(codeChars(" K7QM 4XP2 ")).toBe("K7QM4XP2");
    expect(codeChars("oil")).toBe("011");
    expect(codeChars("K7QM–4XP2")).toBe("K7QM4XP2"); // an en dash from a chat app
    expect(codeChars("K7QU")).toBeNull(); // U is not in the alphabet
    expect(codeChars("K7Q!")).toBeNull();
  });

  it("draws eight cells with a dash after the fourth, and labels one field", () => {
    const { input, cells } = setup();
    expect(cells()).toHaveLength(8);
    expect(input.getAttribute("aria-label")).toBe("Pairing code, 8 characters");
    expect(input.getAttribute("autocomplete")).toBe("one-time-code");
    expect(input.parentElement!.textContent).toContain("–");
  });

  it("types into the next cell, replaces a filled one, and goes back on Backspace", () => {
    const { input, cells, onComplete } = setup();
    for (const key of "k7qm") fireEvent.keyDown(input, { key });
    expect(cells().join("")).toBe("K7QM");
    fireEvent.keyDown(input, { key: "-" }); // the dash is drawn, never typed
    expect(cells().join("")).toBe("K7QM");
    fireEvent.keyDown(input, { key: "Backspace" });
    expect(cells().join("")).toBe("K7Q");
    fireEvent.keyDown(input, { key: "Home" });
    fireEvent.keyDown(input, { key: "x" }); // over the first cell
    expect(cells().join("")).toBe("X7Q");
    fireEvent.keyDown(input, { key: "ArrowRight" });
    fireEvent.keyDown(input, { key: "z" }); // over the third
    expect(cells().join("")).toBe("X7Z");
    expect(onComplete).not.toHaveBeenCalled();
  });

  it("refuses characters outside the alphabet with a shake", () => {
    const { input, cells, shakes } = setup();
    fireEvent.keyDown(input, { key: "u" });
    expect(cells().join("")).toBe("");
    expect(shakes()).toBe(1);
  });

  it("paste fills from the first cell, with or without the dash, and completes", () => {
    const { input, cells, onComplete } = setup();
    paste(input, "k7qm-4xp2");
    expect(cells().join("")).toBe("K7QM4XP2");
    expect(onComplete).toHaveBeenCalledWith("K7QM4XP2");
  });

  it("a pasted pairing link goes to the link path", () => {
    const { input, onLink, onComplete } = setup();
    paste(input, "https://alice.poweur.net/id/approve#pair=K7QM4XP2.abc&id=alice.poweur.net");
    expect(onLink).toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
  });

  it("autofill or a mobile keyboard changing the value is normalized too", () => {
    const { input, onComplete } = setup();
    fireEvent.change(input, { target: { value: "k7qm4xp2" } });
    expect(onComplete).toHaveBeenCalledWith("K7QM4XP2");
  });

  it("puts no length limit on the field itself: spaces around a pasted code must not cut it short", () => {
    const { input, onComplete } = setup();
    expect(input.hasAttribute("maxlength")).toBe(false);
    fireEvent.change(input, { target: { value: "  k7qm-4xp2 " } });
    expect(onComplete).toHaveBeenCalledWith("K7QM4XP2");
  });
});
