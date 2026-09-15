import type { ComponentProps, ReactNode } from "react";
import { cn } from "../lib/cn";

const CHIP_TONES = {
  neutral: "bg-surface-3 text-muted",
  success: "chip-green bg-success/12 text-success",
  warning: "chip-orange bg-warning/12 text-warning",
  danger: "chip-red bg-danger/12 text-danger",
  accent: "chip-accent bg-accent-soft text-accent",
} as const;

export type ChipTone = keyof typeof CHIP_TONES;

export function Chip({ tone = "neutral", className, ...props }: ComponentProps<"span"> & { tone?: ChipTone }) {
  return (
    <span
      className={cn(
        "chip inline-flex items-center gap-1 rounded-full px-2.5 py-[3px] text-xs font-bold",
        CHIP_TONES[tone],
        className,
      )}
      {...props}
    />
  );
}

/** A count on an accent pill (conversation rows, trays). */
export function CountBadge({ count, className }: { count: number; className?: string }) {
  if (!count) return null;
  return (
    <span
      className={cn(
        "conv-badge flex h-5 min-w-5 items-center justify-center rounded-full bg-accent px-[5px] text-[11px] font-bold text-white",
        className,
      )}
    >
      {count > 99 ? "99+" : count}
    </span>
  );
}

export function SectionLabel({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn("section-label px-4 pt-5 pb-2.5 text-[13px] font-bold tracking-[.06em] text-muted uppercase", className)}
      {...props}
    />
  );
}

/** Every destination has one, with the obvious next action. */
export function EmptyState({
  icon,
  title,
  body,
  action,
  className,
}: {
  icon?: ReactNode;
  title: ReactNode;
  body?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("empty-state flex flex-col items-center gap-2.5 px-7 py-11 text-center text-muted", className)}>
      {icon && <div className="empty-state-icon text-[40px]">{icon}</div>}
      <div className="empty-state-title text-[17px] font-bold text-fg">{title}</div>
      {body && <div className="empty-state-body max-w-[30ch] text-sm">{body}</div>}
      {action && <div className="mt-1.5">{action}</div>}
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return (
    <div
      role="presentation"
      className={cn("spinner size-10 animate-spin rounded-full border-3 border-white/20 border-t-white", className)}
    />
  );
}

/** A probe that has not answered yet is a screen about to say something (E15-T12). */
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      aria-hidden="true"
      className={cn(
        "skeleton animate-shimmer rounded-control bg-linear-90 from-surface-2 via-surface-3 to-surface-2 bg-size-[200%_100%]",
        className,
      )}
    />
  );
}

export function Notice({ tone = "info", className, ...props }: ComponentProps<"div"> & { tone?: "info" | "warn" }) {
  return (
    <div
      className={cn(
        "notice mb-3.5 rounded-control px-3.5 py-3 text-[13px] leading-normal text-fg",
        tone === "info" ? "notice-info bg-accent-soft" : "notice-warn bg-warning/14",
        className,
      )}
      {...props}
    />
  );
}
