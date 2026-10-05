import type { ReactNode } from "react";
import { cn } from "../../lib/cn";
import { ThemeToggle } from "../../shell/ThemeToggle";
import { Wordmark } from "../../ui/Logo";

/**
 * A front door is the whole page: no destination nav, because there are no
 * destinations to reach until an identity exists (E15-T7). It stands on
 * poweur.org's ground: the night background and its drifting violet light.
 */
export function DoorPage({ id, className, wide = false, children }: { id: string; className?: string; wide?: boolean; children: ReactNode }) {
  return (
    <div id={id} className={cn("landing relative isolate flex h-dvh flex-col overflow-y-auto bg-bg dark:bg-[#07060b]", className)}>
      <div className="brand-scene -z-10" aria-hidden="true">
        <i className="b1" />
        <i className="b2" />
        <i className="b3" />
        <span className="pane" />
      </div>
      <header
        className={cn(
          "landing-bar mx-auto flex h-header w-full shrink-0 items-center justify-between px-4 pt-safe",
          wide && "md:max-w-[1120px] md:px-8",
        )}
      >
        <Wordmark />
        <ThemeToggle />
      </header>
      <div
        className={cn(
          "landing-body mx-auto flex w-full max-w-[480px] flex-1 flex-col gap-6 px-4 pt-2 pb-[calc(32px+env(safe-area-inset-bottom,0px))] landscape:max-h-[500px]:gap-3.5",
          wide && "md:max-w-[640px] lg:max-w-[1120px] lg:gap-10 lg:px-8 lg:pt-8",
        )}
      >
        {children}
      </div>
    </div>
  );
}

/** A 44px link-styled button (`.btn-link`). */
export function LinkButton({ id, onClick, children }: { id: string; onClick: () => void; children: ReactNode }) {
  return (
    <button
      id={id}
      type="button"
      onClick={onClick}
      className="btn-link min-h-11 px-1 py-2.5 text-sm text-accent hover:underline"
    >
      {children}
    </button>
  );
}

/** A card on the brand ground: frosted, with the hairline border poweur.org's panels use. */
export const cardClass =
  "rounded-card border border-black/5 bg-surface/85 p-5 shadow-card backdrop-blur-xl animate-fade-in-up dark:border-white/8 dark:bg-white/4";
