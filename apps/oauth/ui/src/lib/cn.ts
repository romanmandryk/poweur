import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// Same token names as apps/web, so `rounded-button` and `rounded-control` merge.
const twMerge = extendTailwindMerge({
  extend: {
    theme: {
      color: [
        "bg", "surface", "surface-2", "surface-3", "fg", "muted", "faint", "sep",
        "accent", "accent-soft", "accent-2", "success", "danger", "warning", "chrome",
      ],
      radius: ["card", "control", "button"],
      shadow: ["card", "pop", "accent"],
    },
  },
});

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
