/**
 * The identity's own settings documents: inbox policy and public profile.
 * Loaded once per identity, forced after a save (from app.js).
 */
import { clientFor } from "../lib/client.js";
import { blobToDataUrl, contentTag, extensionFor } from "../lib/avatar-image";
import { primeProfile } from "../lib/profiles.js";
import { relayUrlFor } from "../lib/storage.js";
import type { InboxPolicy } from "../lib/policy";
import { useData } from "../state/data";
import { forgetAvatar, saveLocalAvatar } from "../state/avatars";
import { useSession } from "../state/session";
import { syncOwnAvatar } from "./avatars";

const activeClient = (): any => {
  const identity = useSession.getState().identity;
  return identity ? clientFor(identity) : null;
};

export async function loadPolicy({ force = false } = {}) {
  const current = useData.getState().policy;
  if (current.loading || (current.loaded && !force)) return;
  const client = activeClient();
  if (!client) return;
  useData.setState({ policy: { ...current, loading: true } });
  try {
    const { policy, explicit } = await client.policy();
    useData.setState({ policy: { doc: policy, explicit, loaded: true, loading: false } });
  } catch (error) {
    console.warn("Policy read failed:", (error as Error).message);
    useData.setState((state) => ({ policy: { ...state.policy, loading: false } }));
  }
}

export async function savePolicy(document: InboxPolicy) {
  const client = activeClient();
  if (!client) throw new Error("Unlock your identity first");
  const trusted = document.trusted_auth_services?.length ? [document.trusted_auth_services] : [];
  await client.setPolicy(document.mode, document.anonymous, document.read_receipts, ...trusted);
  await loadPolicy({ force: true });
}

export async function loadProfile({ force = false } = {}) {
  const current = useData.getState().profile;
  if (current.loading || (current.loaded && !force)) return current.doc;
  const client = activeClient();
  if (!client) return null;
  useData.setState({ profile: { ...current, loading: true } });
  try {
    const { profile, explicit } = await client.profile();
    useData.setState({ profile: { doc: profile, explicit, loaded: true, loading: false } });
    void syncOwnAvatar(useSession.getState().identity ?? "", profile?.avatar);
  } catch (error) {
    console.warn("Profile read failed:", (error as Error).message);
    // Loaded either way, or a failed read retries on every render.
    useData.setState((state) => ({ profile: { ...state.profile, loaded: true, loading: false } }));
  }
  return useData.getState().profile.doc;
}

export interface ProfileDraft {
  displayName: string;
  bio: string;
  avatarFile: Blob | null;
  avatarPath: string | null;
  linkLabel: string;
  linkUrl: string;
}

/**
 * Uploaded avatars live in their own folder under the identity's /public. `/pub/`
 * serves a /public folder only once it carries a web-public marker, so this one
 * is marked — and nothing else in /public becomes web-visible because of it.
 */
export const AVATAR_DIR = "public/avatars";
const WEB_PUBLIC_MARKER = ".poweur-web-public";

/** Upload the photo (if one was picked) and write the profile. */
export async function saveProfile(draft: ProfileDraft, onStatus: (text: string) => void = () => {}) {
  const identity = useSession.getState().identity;
  const client = activeClient();
  if (!identity || !client) throw new Error("Unlock your identity first");

  const previous: string | null = useData.getState().profile.doc?.avatar ?? null;
  let avatarPath = draft.avatarPath;
  let avatarDataUrl: string | null = null;
  if (draft.avatarFile) {
    onStatus("Uploading photo…");
    const dav = await client.dav();
    // A content-named file: a new photo is a new URL, never a stale cached one.
    const path = `${AVATAR_DIR}/avatar-${await contentTag(draft.avatarFile)}.${extensionFor(draft.avatarFile.type)}`;
    await dav.mkdir(AVATAR_DIR).catch(() => {}); // already there
    await dav.write(`${AVATAR_DIR}/${WEB_PUBLIC_MARKER}`, "");
    await dav.write(path, draft.avatarFile);
    avatarPath = path;
    avatarDataUrl = await blobToDataUrl(draft.avatarFile).catch(() => null);
  }
  onStatus("Saving…");
  const saved = await client.setProfile({
    version: 1,
    display_name: draft.displayName,
    bio: draft.bio,
    ...(avatarPath ? { avatar: avatarPath } : {}),
    links: [{ label: draft.linkLabel, url: draft.linkUrl }],
  });
  useData.setState({ profile: { doc: saved, explicit: true, loaded: true, loading: false } });
  // Every circle of ours shows the new photo at once, on every screen.
  if (saved.avatar && avatarDataUrl) saveLocalAvatar(identity, saved.avatar, avatarDataUrl);
  else if (!saved.avatar) forgetAvatar(identity);
  // A replaced or removed upload is not left behind in /public.
  if (previous && previous !== saved.avatar && previous.startsWith(`${AVATAR_DIR}/`)) {
    void client
      .dav()
      .then((dav: any) => dav.remove(previous))
      .catch(() => {});
  }
  // Every card that shows us should show the new name immediately.
  primeProfile(identity, saved, relayUrlFor(identity));
  return saved;
}
