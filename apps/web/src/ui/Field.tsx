import type { ComponentProps, ReactNode } from "react";
import { cn } from "../lib/cn";

const CONTROL =
  "input w-full appearance-none rounded-control border-[1.5px] border-sep bg-surface px-4 py-[13px] text-base text-fg " +
  "outline-none transition-[border-color,box-shadow] placeholder:text-faint " +
  "focus:border-accent focus:shadow-[0_0_0_3px_var(--color-accent-soft)]";

export const inputClass = CONTROL;

export function Input({ className, ...props }: ComponentProps<"input">) {
  return <input className={cn(CONTROL, className)} {...props} />;
}

export function Textarea({ className, ...props }: ComponentProps<"textarea">) {
  return <textarea className={cn(CONTROL, "min-h-40 resize-none p-3.5 leading-normal", className)} {...props} />;
}

export function Label({ className, ...props }: ComponentProps<"label">) {
  return (
    <label
      className={cn("form-label mb-1.5 block text-[13px] font-semibold tracking-[.05em] text-muted uppercase", className)}
      {...props}
    />
  );
}

export function FormGroup({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("form-group mb-4", className)} {...props} />;
}

/** Small explanatory text under a form or button. */
export function Note({ className, ...props }: ComponentProps<"p">) {
  return <p className={cn("form-note mt-5 text-center text-[13px] leading-normal text-muted", className)} {...props} />;
}

/** A checkbox row: control on the left, label and detail beside it. */
export function CheckRow({
  id,
  checked,
  onCheckedChange,
  label,
  detail,
  className,
}: {
  id: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  label: ReactNode;
  detail?: ReactNode;
  className?: string;
}) {
  return (
    <label htmlFor={id} className={cn("policy-toggle flex min-h-11 cursor-pointer items-start gap-3 py-3", className)}>
      <input
        id={id}
        type="checkbox"
        className="policy-check mt-0.5 size-[22px] shrink-0 accent-accent"
        checked={checked}
        onChange={(event) => onCheckedChange(event.currentTarget.checked)}
      />
      <span>
        <div className="policy-toggle-label text-[15px] font-semibold">{label}</div>
        {detail && <div className="policy-toggle-detail text-[13px] leading-snug text-muted">{detail}</div>}
      </span>
    </label>
  );
}
