/**
 * Keeping the avatar store (`state/avatars`) filled: our own photo read back
 * from our system files when the profile names one this device does not hold yet, and other
 * people's resolved from their public profile by the screens that list them.
 */
import { useEffect } from "react";
import { blobToDataUrl, squareAvatar } from "../lib/avatar-image";
import { forgetAvatar, readLocalAvatar, rememberAvatar, saveLocalAvatar } from "../state/avatars";
import { useSession } from "../state/session";
import { activeClient, resolveForActive } from "./relay";

/** Profiles are cached for as long by `lib/profiles.js`; asking sooner is wasted. */
const PEER_TTL_MS = 5 * 60_000;
/** The largest copy worth keeping in localStorage when it could not be scaled down. */
const MAX_LOCAL_BYTES = 200 * 1024;

const MIME: Record<string, string> = { jpg: "image/jpeg", jpeg: "image/jpeg", png: "image/png", webp: "image/webp", gif: "image/gif" };
const peersAskedAt = new Map<string, number>();

/** Our own avatar, in step with the profile document (a new device reads it once). */
export async function syncOwnAvatar(identity: string, avatarPath: string | null | undefined) {
  const key = identity.trim().toLowerCase();
  if (!key || key !== String(useSession.getState().identity ?? "").toLowerCase()) return;
  if (!avatarPath) {
    if (readLocalAvatar(key)) forgetAvatar(key);
    return;
  }
  if (readLocalAvatar(key)?.path === avatarPath) return;
  const client: any = activeClient();
  if (!client) return;
  try {
    const file = await client.system().get(`.poweur/public/${avatarPath}`);
    if (!file) return;
    const bytes: Uint8Array = file.bytes;
    const extension = avatarPath.split(".").pop()?.toLowerCase() ?? "";
    const scaled = await squareAvatar(new Blob([bytes as BlobPart], { type: MIME[extension] ?? "image/jpeg" }));
    if (scaled.size > MAX_LOCAL_BYTES) return;
    saveLocalAvatar(key, avatarPath, await blobToDataUrl(scaled));
  } catch (error) {
    console.warn("Avatar read failed:", (error as Error).message);
  }
}

/** Someone else's avatar, from their public profile. Initials when it cannot be had. */
export async function loadPeerAvatar(identity: string) {
  const key = identity.trim().toLowerCase();
  if (!key || readLocalAvatar(key)) return;
  const asked = peersAskedAt.get(key);
  if (asked && Date.now() - asked < PEER_TTL_MS) return;
  peersAskedAt.set(key, Date.now());
  try {
    const entry: any = await resolveForActive(key);
    rememberAvatar(key, entry?.avatar ?? null);
  } catch {
    // Unresolvable right now: the circle keeps its initials.
  }
}

/** Resolve the avatars of the people a screen shows. */
export function usePeerAvatars(identities: readonly string[]) {
  const wanted = [...new Set(identities.map((identity) => identity.trim().toLowerCase()).filter(Boolean))].sort().join(" ");
  useEffect(() => {
    for (const identity of wanted.split(" ").filter(Boolean)) void loadPeerAvatar(identity);
  }, [wanted]);
}

export function resetPeerAvatarsForTests() {
  peersAskedAt.clear();
}
