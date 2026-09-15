/**
 * Identity presentation helpers, from apps/web/js/components/dom.js and
 * identity-input.js. Pure functions: no DOM, no storage.
 */

const AVATAR_PALETTE = [
  "#5856D6", "#FF2D55", "#FF9500", "#34C759",
  "#007AFF", "#AF52DE", "#FF3B30", "#00C7BE",
];

/** A stable colour per identity, the same one the legacy app shows. */
export function avatarColor(identity: string): string {
  let hash = 0;
  for (const character of String(identity)) hash = (hash * 31 + character.charCodeAt(0)) >>> 0;
  return AVATAR_PALETTE[hash % AVATAR_PALETTE.length];
}

export const handleOf = (fqdn?: string | null): string => String(fqdn || "").split(".")[0] || "";
export const domainOf = (fqdn?: string | null): string => String(fqdn || "").split(".").slice(1).join(".");
export const initialOf = (fqdn?: string | null): string => (handleOf(fqdn)[0] || "?").toUpperCase();

/** The relay's own rule, mirrored client-side so typos surface before a request. */
const IDENTITY_RE = /^(?=.{3,253}$)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/;

export function isValidIdentity(value: unknown): boolean {
  return IDENTITY_RE.test(String(value || "").trim().toLowerCase());
}
