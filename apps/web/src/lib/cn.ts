import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// tailwind-merge only knows Tailwind's default scale; without the project's
// token names `rounded-button` and `rounded-control` would both survive a
// merge and CSS order would pick the winner.
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

/** Merge Tailwind classes; later classes win over conflicting earlier ones. */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
