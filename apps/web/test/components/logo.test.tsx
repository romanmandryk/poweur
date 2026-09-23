import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { GLASS_MIN_HEIGHT, Logo } from "../../src/ui/Logo";
import { Welcome } from "../../src/screens/gates";

const images = (el: Element) => [...el.querySelectorAll("img")];

describe("Logo (E15-T14)", () => {
  it("draws the glass P at large sizes, keeping the artwork's 600:760 proportions", () => {
    const { container } = render(<Logo variant="glass" height={76} />);
    const [img] = images(container);
    expect(images(container)).toHaveLength(1);
    expect(img.getAttribute("src")).toMatch(/p-glass/);
    expect(img).toHaveAttribute("width", "60");
    expect(img).toHaveAttribute("height", "76");
    expect(img).toHaveAttribute("alt", "Poweur");
  });

  it("falls back to the flat mark below the size the glass lighting survives", () => {
    const { container } = render(<Logo variant="glass" height={GLASS_MIN_HEIGHT - 1} />);
    expect(images(container).map((img) => img.getAttribute("src"))).toEqual([
      // Small SVGs are inlined as data: URIs by Vite, so match the file or its fill.
      expect.stringMatching(/p-black|%23000000/),
      expect.stringMatching(/p-white|%23FFFFFF/),
    ]);
  });

  it("swaps black for white in the dark theme with CSS alone", () => {
    const { container } = render(<Logo height={24} />);
    const [black, white] = images(container);
    expect(black.className).toContain("dark:hidden");
    expect(white.className).toMatch(/\bhidden\b/);
    expect(white.className).toContain("dark:block");
  });

  it("can be decorative beside the product name", () => {
    const { container } = render(<Logo height={24} alt="" />);
    for (const img of images(container)) expect(img).toHaveAttribute("alt", "");
  });
});

describe("Welcome gate", () => {
  it("shows the glass P on the brand glow instead of a generic glyph", () => {
    render(<Welcome />);
    const logo = screen.getByRole("img", { name: "Poweur" });
    expect(logo.getAttribute("src")).toMatch(/p-glass/);
    expect(logo.closest(".welcome-icon")?.className).toContain("bg-brand-glow");
    expect(document.querySelector(".welcome-icon svg")).toBeNull();
  });
});
