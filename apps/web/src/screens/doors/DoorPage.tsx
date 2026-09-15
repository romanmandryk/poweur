import type { ReactNode } from "react";
import { cn } from "../../lib/cn";
import { ThemeToggle } from "../../shell/ThemeToggle";

/**
 * A front door is the whole page: no destination nav, because there are no
 * destinations to reach until an identity exists (E15-T7).
 */
export function DoorPage({ id, className, children }: { id: string; className?: string; children: ReactNode }) {
  return (
    <div id={id} className={cn("landing flex h-dvh flex-col overflow-y-auto bg-bg", className)}>
      <header className="landing-bar flex h-header shrink-0 items-center justify-between px-4 pt-safe">
        <span className="app-wordmark text-[17px] font-bold tracking-[-.3px]">Poweur ID</span>
        <ThemeToggle />
      </header>
      <div className="landing-body mx-auto flex w-full max-w-[460px] flex-1 flex-col gap-6 px-5 pt-2 pb-[calc(32px+env(safe-area-inset-bottom,0px))] landscape:max-h-[500px]:gap-3.5">
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

export const cardClass = "rounded-card bg-surface p-5 shadow-card animate-fade-in-up";
