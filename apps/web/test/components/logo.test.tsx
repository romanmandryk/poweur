import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { GLASS_MIN_HEIGHT, Logo, Wordmark } from "../../src/ui/Logo";
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

describe("Wordmark", () => {
  it("is poweur.org's horizontal lockup, black on light and white on dark", () => {
    const { container } = render(<Wordmark />);
    const [black, white] = images(container);
    expect(images(container)).toHaveLength(2);
    expect(black.getAttribute("src")).toMatch(/lockup-black|%23000000/);
    expect(white.getAttribute("src")).toMatch(/lockup-white|%23FFFFFF/);
    expect(black.className).toContain("dark:hidden");
    expect(white.className).toContain("dark:block");
    // The artwork is 3164 x 864.
    expect(black).toHaveAttribute("width", "81");
    expect(black).toHaveAttribute("alt", "Poweur");
  });
});

describe("Welcome gate", () => {
  it("shows the glass P on its glow, as poweur.org does, not an app-icon tile", () => {
    render(<Welcome />);
    const logo = screen.getByRole("img", { name: "Poweur" });
    expect(logo.getAttribute("src")).toMatch(/p-glass/);
    expect(logo.closest(".welcome-icon")?.querySelector(".brand-mark-glow")).toBeTruthy();
    expect(logo.closest(".welcome-icon")?.className).not.toContain("bg-brand-glow");
    expect(document.querySelector(".welcome-icon svg")).toBeNull();
  });
});
