import type { KeyboardEvent } from "react";

/**
 * A row that is a `role="button"` div: a button to a screen reader and a dead
 * end to a keyboard unless it answers Enter and Space itself. Ignores keys
 * aimed at a real control nested inside the row.
 */
export function onActivateKeys(action: () => void) {
  return (event: KeyboardEvent) => {
    if (event.target !== event.currentTarget) return;
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    action();
  };
}
