/** Time formatting shared by the tray rows and the conversation view (from app.js). */

export function fmtRelative(timestamp: string, now = Date.now()): string {
  const delta = now - new Date(timestamp).getTime();
  if (Number.isNaN(delta)) return "";
  if (delta < 60_000) return "now";
  if (delta < 3_600_000) return `${Math.floor(delta / 60_000)}m`;
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h`;
  return `${Math.floor(delta / 86_400_000)}d`;
}

export function fmtClock(timestamp: string): string {
  const date = new Date(timestamp);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/** "Today", "Yesterday", or a short date — the separator between days in a thread. */
export function dayLabel(timestamp: string, today = new Date()): string {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return "";
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  if (date.toDateString() === today.toDateString()) return "Today";
  if (date.toDateString() === yesterday.toDateString()) return "Yesterday";
  return date.toLocaleDateString(undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
    ...(date.getFullYear() !== today.getFullYear() ? { year: "numeric" } : {}),
  });
}
