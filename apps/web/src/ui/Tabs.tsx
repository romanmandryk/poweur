import type { ComponentProps } from "react";
import { cn } from "../lib/cn";

/** Underline tabs: a few views of the same place (message trays, whose files). */
export function TabBar({ className, ...props }: ComponentProps<"div">) {
  return <div role="tablist" className={cn("tab-bar flex border-b border-sep px-4", className)} {...props} />;
}

export function Tab({ active, className, children, ...props }: ComponentProps<"button"> & { active: boolean }) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      className={cn(
        "tray-tab -mb-px flex min-h-11 flex-1 items-center justify-center gap-1.5 border-b-2 border-transparent px-3 text-[15px] font-semibold text-muted transition-colors focus-visible:-outline-offset-2 [@media(hover:hover)]:hover:text-fg",
        active && "active border-accent text-accent [@media(hover:hover)]:hover:text-accent",
        className,
      )}
      {...props}
    >
      {children}
    </button>
  );
}
