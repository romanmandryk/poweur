export function formatDate(iso?: string, withTime = false): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString(undefined, withTime
    ? { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" }
    : { day: "numeric", month: "short", year: "numeric" });
}

export function scopeLabel(scope: string): string {
  return scope === "poweur_id" ? "Poweur ID" : scope === "profile" ? "Name and photo" : scope;
}
