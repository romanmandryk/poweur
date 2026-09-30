import { cn } from "../lib/cn";
import glassUrl from "../assets/brand/p-glass.svg";
import blackUrl from "../assets/brand/p-black.svg";
import whiteUrl from "../assets/brand/p-white.svg";
import lockupBlackUrl from "../assets/brand/lockup-black.svg";
import lockupWhiteUrl from "../assets/brand/lockup-white.svg";

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

/** The lockup's artwork is 3164 × 864 (design/brand/scripts/lockup.py); width follows height. */
const lockupWidthFor = (height: number) => Math.round((height * 3164) / 864);

/**
 * The P and the Sora wordmark, outlined — the lockup poweur.org's nav uses.
 * Black in the light theme and white in the dark one, swapped with CSS alone.
 */
export function Wordmark({ className, height = 22 }: { className?: string; height?: number }) {
  const size = { width: lockupWidthFor(height), height };
  return (
    <span className={cn("app-brand logo-lockup inline-flex shrink-0 items-center", className)}>
      <img src={lockupBlackUrl} {...size} alt="Poweur" draggable={false} className="select-none dark:hidden" />
      <img src={lockupWhiteUrl} {...size} alt="Poweur" draggable={false} className="hidden select-none dark:block" />
    </span>
  );
}

/**
 * The glass P floating on the brand glow, as poweur.org draws it: no tile,
 * just the mark and a soft violet light behind it.
 */
export function BrandMark({ className, height = 88 }: { className?: string; height?: number }) {
  return (
    <div className={cn("brand-mark relative flex animate-pop-in items-center justify-center", className)}>
      <div
        aria-hidden="true"
        className="brand-mark-glow absolute inset-[-30%] rounded-full bg-[radial-gradient(circle,var(--color-violet-500),var(--color-indigo-800)_55%,transparent_72%)] opacity-60 blur-2xl dark:opacity-80"
      />
      <Logo variant="glass" height={height} alt="Poweur" className="relative drop-shadow-[0_18px_36px_rgb(0_0_0/.45)]" />
    </div>
  );
}
