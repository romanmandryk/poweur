import type { ComponentProps, ReactNode } from "react";
import { cn } from "../lib/cn";

const CONTROL =
  "w-full appearance-none rounded-control border-[1.5px] border-sep bg-surface px-4 py-3 text-base text-fg " +
  "outline-none transition-[border-color,box-shadow] placeholder:text-faint " +
  "focus:border-accent focus:shadow-[0_0_0_3px_var(--color-accent-soft)]";

export const inputClass = CONTROL;

export function Input({ className, ...props }: ComponentProps<"input">) {
  return <input className={cn(CONTROL, className)} {...props} />;
}

export function Textarea({ className, ...props }: ComponentProps<"textarea">) {
  return <textarea className={cn(CONTROL, "resize-y leading-normal", className)} {...props} />;
}

export function Label({ className, ...props }: ComponentProps<"label">) {
  return <label className={cn("mb-1.5 block text-[13px] font-semibold text-muted", className)} {...props} />;
}

export function Hint({ className, ...props }: ComponentProps<"p">) {
  return <p className={cn("mt-1.5 text-[13px] leading-snug text-muted", className)} {...props} />;
}

export function Field({ label, htmlFor, hint, children, className }: {
  label: ReactNode;
  htmlFor: string;
  hint?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("mb-4", className)}>
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint && <Hint>{hint}</Hint>}
    </div>
  );
}

export function ErrorLine({ children, className }: { children?: ReactNode; className?: string }) {
  if (!children) return null;
  return (
    <p role="alert" className={cn("mt-2 text-sm font-medium text-danger", className)}>
      {children}
    </p>
  );
}
