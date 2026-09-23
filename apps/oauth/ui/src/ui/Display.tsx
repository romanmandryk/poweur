import { useState, type ComponentProps, type ReactNode } from "react";
import { Check, ChevronDown, Copy, type LucideIcon } from "lucide-react";
import { cn } from "../lib/cn";

export function Card({ className, ...props }: ComponentProps<"section">) {
  return <section className={cn("rounded-card bg-surface p-5 shadow-card sm:p-6", className)} {...props} />;
}

const CHIP_TONES = {
  neutral: "bg-surface-3 text-muted",
  success: "bg-success/12 text-success",
  warning: "bg-warning/12 text-warning",
  danger: "bg-danger/12 text-danger",
  accent: "bg-accent-soft text-accent",
} as const;

export function Chip({ tone = "neutral", className, ...props }: ComponentProps<"span"> & { tone?: keyof typeof CHIP_TONES }) {
  return (
    <span
      className={cn("inline-flex items-center gap-1 rounded-full px-2.5 py-[3px] text-xs font-bold", CHIP_TONES[tone], className)}
      {...props}
    />
  );
}

export function Spinner({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn("inline-block size-4 animate-spin rounded-full border-2 border-current border-t-transparent", className)}
    />
  );
}

/** A round initial: an application or an identity, without fetching images. */
export function Monogram({ text, className }: { text: string; className?: string }) {
  const letter = (text.trim().replace(/^https?:\/\//, "")[0] || "?").toUpperCase();
  return (
    <span
      aria-hidden="true"
      className={cn(
        "flex size-12 shrink-0 items-center justify-center rounded-[14px] bg-linear-135 from-accent to-accent-2 text-xl font-bold text-white",
        className,
      )}
    >
      {letter}
    </span>
  );
}

export function IconTile({ icon: Icon, className }: { icon: LucideIcon; className?: string }) {
  return (
    <span className={cn("flex size-10 shrink-0 items-center justify-center rounded-[12px] bg-accent-soft text-accent", className)}>
      <Icon className="size-5" strokeWidth={1.9} aria-hidden="true" />
    </span>
  );
}

export function CopyButton({ value, label = "Copy", className }: { value: string; label?: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-control bg-surface-2 px-3 py-1.5 text-sm font-semibold text-fg",
        className,
      )}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1600);
        } catch {
          /* the value is on screen to select by hand */
        }
      }}
    >
      {copied ? <Check className="size-4 text-success" aria-hidden="true" /> : <Copy className="size-4" aria-hidden="true" />}
      {copied ? "Copied" : label}
    </button>
  );
}

/** A value to copy: monospace, wraps, with its button. */
export function CopyValue({ value, label, id }: { value: string; label?: string; id?: string }) {
  return (
    <div className="flex items-center gap-2 rounded-control bg-surface-2 py-1.5 pr-1.5 pl-3">
      <code id={id} className="min-w-0 flex-1 font-mono text-[13px] break-all">
        {value}
      </code>
      <CopyButton value={value} label={label} />
    </div>
  );
}

export function Disclosure({ summary, children, defaultOpen = false, className, id }: {
  summary: ReactNode;
  children: ReactNode;
  defaultOpen?: boolean;
  className?: string;
  id?: string;
}) {
  return (
    <details id={id} open={defaultOpen} className={cn("group", className)}>
      <summary className="flex cursor-pointer list-none items-center gap-1.5 text-sm font-semibold text-accent select-none [&::-webkit-details-marker]:hidden">
        {summary}
        <ChevronDown className="size-4 transition-transform group-open:rotate-180" aria-hidden="true" />
      </summary>
      <div className="pt-3">{children}</div>
    </details>
  );
}

export function Banner({ tone = "success", children, className }: {
  tone?: "success" | "warning" | "danger";
  children: ReactNode;
  className?: string;
}) {
  return (
    <p
      role="status"
      className={cn(
        "rounded-control px-4 py-3 text-sm font-medium",
        tone === "success" && "bg-success/12 text-success",
        tone === "warning" && "bg-warning/12 text-warning",
        tone === "danger" && "bg-danger/12 text-danger",
        className,
      )}
    >
      {children}
    </p>
  );
}
