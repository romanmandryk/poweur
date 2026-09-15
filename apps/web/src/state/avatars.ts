/**
 * Avatar images by identity, for every circle the app draws.
 *
 * Two sources. An identity on this device keeps a small copy of its own photo
 * (a data URL, ≈20 KB at 256 px) in localStorage — written when it is uploaded
 * or first read back over DAV — so the unlock screen and the identity switcher
 * show it before any key is open. Everyone else's comes from their public
 * profile, resolved by the screens that list them (`actions/avatars`).
 *
 * No app imports: `ui/Avatar` reads this, and it stays embeddable.
 */
import { create } from "zustand";

const LOCAL_PREFIX = "poweur:avatar:";

export interface LocalAvatar {
  path: string;
  dataUrl: string;
}

const keyOf = (identity: string | null | undefined) => String(identity ?? "").trim().toLowerCase();

/** identity → image URL, or null when it is known to have none. */
export const useAvatars = create<{ urls: Record<string, string | null> }>()(() => ({ urls: {} }));

function setUrl(key: string, url: string | null) {
  useAvatars.setState((state) => (state.urls[key] === url ? state : { urls: { ...state.urls, [key]: url } }));
}

export function readLocalAvatar(identity: string | null | undefined): LocalAvatar | null {
  const key = keyOf(identity);
  if (!key) return null;
  try {
    const raw = globalThis.localStorage?.getItem(LOCAL_PREFIX + key);
    if (!raw) return null;
    const value = JSON.parse(raw);
    // Only ever an image we encoded ourselves; anything else is ignored.
    return typeof value?.path === "string" && typeof value?.dataUrl === "string" && value.dataUrl.startsWith("data:image/")
      ? { path: value.path, dataUrl: value.dataUrl }
      : null;
  } catch {
    return null;
  }
}

/** Our own photo, kept on this device and shown at once everywhere. */
export function saveLocalAvatar(identity: string, path: string, dataUrl: string) {
  const key = keyOf(identity);
  if (!key) return;
  try {
    localStorage.setItem(LOCAL_PREFIX + key, JSON.stringify({ path, dataUrl }));
  } catch {
    // Storage full or unavailable: this session still shows it.
  }
  setUrl(key, dataUrl);
}

export function forgetAvatar(identity: string) {
  const key = keyOf(identity);
  if (!key) return;
  try {
    localStorage.removeItem(LOCAL_PREFIX + key);
  } catch {
    // Nothing stored to begin with.
  }
  setUrl(key, null);
}

/** Someone's public avatar URL, once resolved. A copy kept on this device wins. */
export function rememberAvatar(identity: string, url: string | null) {
  const key = keyOf(identity);
  if (!key || readLocalAvatar(key)) return;
  setUrl(key, url);
}

/** The image for an identity's circle, or null for initials. */
export function useAvatarSrc(identity: string | null | undefined): string | null {
  const key = keyOf(identity);
  const url = useAvatars((state) => state.urls[key]);
  if (url !== undefined) return url;
  return readLocalAvatar(key)?.dataUrl ?? null;
}

export function resetAvatarsForTests() {
  useAvatars.setState({ urls: {} });
}
