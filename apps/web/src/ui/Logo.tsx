import { cn } from "../lib/cn";
import glassUrl from "../assets/brand/p-glass.svg";
import blackUrl from "../assets/brand/p-black.svg";
import whiteUrl from "../assets/brand/p-white.svg";

/** Below this the glass lighting turns to mush (design/brand/README.md); flat takes over. */
export const GLASS_MIN_HEIGHT = 48;

/** The P's artwork is 600 × 760; width follows height. */
const widthFor = (height: number) => Math.round((height * 600) / 760);

/**
 * The Poweur P. `glass` is the black glass mark for large placements; `flat` is
 * the one-colour mark, black in the light theme and white in the dark one.
 * The swap is CSS-only (`dark:`), so there is no flash while the theme loads.
 * Pass `alt=""` when the mark sits beside the product name and would only repeat it.
 */
export function Logo({
  variant = "flat",
  height,
  alt = "Poweur",
  className,
}: {
  variant?: "glass" | "flat";
  height: number;
  alt?: string;
  className?: string;
}) {
  const size = { width: widthFor(height), height };
  if (variant === "glass" && height >= GLASS_MIN_HEIGHT) {
    return <img src={glassUrl} {...size} alt={alt} draggable={false} className={cn("logo logo-glass select-none", className)} />;
  }
  return (
    <span className={cn("logo logo-flat inline-flex shrink-0", className)}>
      <img src={blackUrl} {...size} alt={alt} draggable={false} className="select-none dark:hidden" />
      <img src={whiteUrl} {...size} alt={alt} draggable={false} className="hidden select-none dark:block" />
    </span>
  );
}

/** The P and the product name, as the header and the front doors show them. */
export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={cn("app-brand flex items-center gap-2", className)}>
      <Logo height={24} alt="" />
      <span className="app-wordmark text-[17px] font-bold tracking-[-.3px]">Poweur ID</span>
    </span>
  );
}

/** The glass P on the brand glow: the app icon, drawn in the page. */
export function BrandTile({ className }: { className?: string }) {
  return (
    <div
      className={cn(
        "brand-tile flex size-24 animate-pop-in items-center justify-center overflow-hidden rounded-[28px] bg-brand-glow shadow-[0_12px_40px_color-mix(in_srgb,var(--color-violet-600)_35%,transparent)]",
        className,
      )}
    >
      <Logo variant="glass" height={62} alt="Poweur" />
    </div>
  );
}
