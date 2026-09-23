import type { ComponentProps } from "react";
import { cn } from "../lib/cn";

// The web app's button (apps/web/src/ui/Button.tsx), plus an anchor form:
// most of the bridge's actions are navigations or form posts.
const BASE =
  "inline-flex items-center justify-center gap-2 font-semibold whitespace-nowrap select-none no-underline " +
  "transition-[opacity,transform] duration-150 active:not-disabled:scale-[.98] active:not-disabled:opacity-90 " +
  "disabled:cursor-not-allowed disabled:opacity-40 [@media(hover:hover)]:hover:not-disabled:opacity-90";

const VARIANTS = {
  primary: "w-full rounded-button bg-accent px-4 py-3.5 text-[17px] text-white",
  secondary: "w-full rounded-button bg-surface-2 px-4 py-3.5 text-base text-fg",
  outline: "w-full rounded-button border-[1.5px] border-sep bg-surface px-4 py-3 text-base text-fg",
  danger: "rounded-control bg-danger/12 px-5 py-3 text-base text-danger",
  link: "min-h-8 rounded-control bg-transparent px-0.5 py-1.5 text-sm font-medium text-accent",
} as const;

export type ButtonVariant = keyof typeof VARIANTS;

type Common = { variant?: ButtonVariant; size?: "md" | "sm" };

function classes(variant: ButtonVariant, size: "md" | "sm", className?: string) {
  return cn(
    BASE,
    VARIANTS[variant],
    size === "sm" && "min-h-9 w-auto rounded-control px-3.5 py-1.5 text-sm",
    className,
  );
}

export function Button({ variant = "primary", size = "md", type = "button", className, ...props }: ComponentProps<"button"> & Common) {
  return <button type={type} className={classes(variant, size, className)} {...props} />;
}

export function LinkButton({ variant = "primary", size = "md", className, ...props }: ComponentProps<"a"> & Common) {
  return <a className={classes(variant, size, className)} {...props} />;
}
