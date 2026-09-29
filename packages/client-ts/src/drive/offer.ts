/**
 * Share offers (PCP-0008); Go packages/identity/drive/offer.go is
 * authoritative. An offer carries the signed share, the drive's relay and
 * the shared node's name and kind to the member, end-to-end encrypted.
 * Nothing here grants access: the share on the relay does.
 */
import { validateShare, type Share } from "./share.js";
import { normalizeName } from "./names.js";

export const OFFER_FORMAT = 2;
export interface ShareOffer { format: number; share: Share; relay: string; name?: string; kind: "file" | "folder"; offered_at: string }
export interface ShareAccept { format: number; drive: string; share_id: string; accepted_at: string }
export interface ShareRevoked { format: number; drive: string; share_id: string; revoked_at: string }
export interface Mount { drive: string; relay: string; node: string; share_id: string; name: string; kind: string; role: string; mounted: string }
export interface Mounts { format: number; mounts: Mount[] }

const time = (value: string) => /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(value) && !Number.isNaN(Date.parse(value));
const hex16 = (value: string) => /^[0-9a-f]{32}$/.test(value);
const identity = (value: string) => typeof value === "string" && value === value.trim().toLowerCase() && value.includes(".");
const relay = (value: string) => {
  const host = value.replace(/^https?:\/\//, "");
  return host.length > 0 && host.length <= 253 && !/[\/ \\\0?#@]/.test(host);
};

export function validateShareOffer(o: ShareOffer): void {
  if (o.format !== OFFER_FORMAT) throw new Error("unsupported share offer format");
  validateShare(o.share);
  if (!o.share.member) throw new Error("an offer is for a member; links travel as URLs");
  if (!relay(o.relay)) throw new Error("invalid relay");
  if (o.kind !== "file" && o.kind !== "folder") throw new Error("kind is file or folder");
  if (o.name) normalizeName(o.name);
  if (!time(o.offered_at)) throw new Error("invalid offer time");
}
export function validateShareAccept(a: ShareAccept): void {
  if (a.format !== OFFER_FORMAT || !identity(a.drive) || !hex16(a.share_id) || !time(a.accepted_at)) throw new Error("invalid share accept");
}
export function validateShareRevoked(r: ShareRevoked): void {
  if (r.format !== OFFER_FORMAT || !identity(r.drive) || !hex16(r.share_id) || !time(r.revoked_at)) throw new Error("invalid share revocation notice");
}

/** Add (or refresh) the mount for an offer; returns the accept body to send. */
export function acceptOffer(mounts: Mounts, o: ShareOffer, now = new Date()): ShareAccept {
  validateShareOffer(o);
  const stamp = now.toISOString().replace(/\.\d{3}Z$/, "Z");
  mounts.format = 1;
  mounts.mounts = mounts.mounts.filter(m => m.drive !== o.share.drive || m.node !== o.share.node);
  mounts.mounts.push({ drive: o.share.drive, relay: o.relay, node: o.share.node, share_id: o.share.id, name: o.name ?? "", kind: o.kind, role: o.share.role, mounted: stamp });
  return { format: OFFER_FORMAT, drive: o.share.drive, share_id: o.share.id, accepted_at: stamp };
}
/** Remove the mount a revocation notice names; reports whether one went. */
export function dropMount(mounts: Mounts, r: ShareRevoked): boolean {
  const before = mounts.mounts.length;
  mounts.mounts = mounts.mounts.filter(m => m.drive !== r.drive || m.share_id !== r.share_id);
  return mounts.mounts.length !== before;
}
