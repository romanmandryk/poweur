import type { ComponentProps } from "react";
import { cn } from "../lib/cn";

/*
 * `btn` / `btn-<variant>` stay on the element as hooks: the Playwright suite
 * and panel autofocus (`.btn-primary`) find buttons by them. Styling is all
 * Tailwind.
 */
const BASE =
  "btn inline-flex items-center justify-center gap-2 font-semibold whitespace-nowrap select-none " +
  "transition-[opacity,transform] duration-150 active:not-disabled:scale-[.98] active:not-disabled:opacity-90 " +
  "disabled:cursor-not-allowed disabled:opacity-40 [@media(hover:hover)]:hover:not-disabled:opacity-90";

const VARIANTS = {
  primary: "w-full rounded-button bg-accent p-4 text-[17px] text-white",
  passkey:
    "relative w-full overflow-hidden rounded-button bg-linear-135 from-accent to-accent-2 p-4 text-[17px] text-white shadow-accent",
  ghost: "w-full rounded-button bg-surface-2 p-3.5 text-base text-fg",
  secondary: "rounded-control bg-surface-2 px-6 py-3.5 text-base text-fg",
  danger: "rounded-control bg-danger/12 px-6 py-3.5 text-base text-danger",
  link: "min-h-8 rounded-control bg-transparent px-0.5 py-1.5 text-[13px] font-normal text-accent",
} as const;

export type ButtonVariant = keyof typeof VARIANTS;

export type ButtonProps = ComponentProps<"button"> & {
  variant?: ButtonVariant;
  size?: "md" | "sm";
};

export function Button({ variant = "primary", size = "md", type = "button", className, ...props }: ButtonProps) {
  return (
    <button
      type={type}
      className={cn(
        BASE,
        `btn-${variant}`,
        VARIANTS[variant],
        size === "sm" && "btn-sm min-h-10 w-auto rounded-control px-4 py-2 text-sm",
        className,
      )}
      {...props}
    />
  );
}

export function IconButton({ type = "button", className, ...props }: ComponentProps<"button">) {
  return (
    <button
      type={type}
      className={cn(
        "btn-icon flex size-9 shrink-0 items-center justify-center rounded-full bg-surface-2 text-base text-fg " +
          "transition-[background-color,transform] active:scale-[.93] active:bg-surface-3 " +
          "[@media(hover:hover)]:hover:bg-surface-3",
        className,
      )}
      {...props}
    />
  );
}
