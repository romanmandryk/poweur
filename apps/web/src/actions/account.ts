/**
 * The identity's own settings documents: inbox policy and public profile.
 * Loaded once per identity, forced after a save (from app.js).
 */
import { clientFor } from "../lib/client.js";
import { primeProfile } from "../lib/profiles.js";
import { relayUrlFor } from "../lib/storage.js";
import type { InboxPolicy } from "../lib/policy";
import { useData } from "../state/data";
import { useSession } from "../state/session";

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
  await client.setPolicy(document.mode, document.anonymous, document.read_receipts);
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
  avatarFile: File | null;
  avatarPath: string | null;
  linkLabel: string;
  linkUrl: string;
}

/** Upload the avatar (into the identity's own /public) and write the profile. */
export async function saveProfile(draft: ProfileDraft, onStatus: (text: string) => void = () => {}) {
  const identity = useSession.getState().identity;
  const client = activeClient();
  if (!identity || !client) throw new Error("Unlock your identity first");

  let avatarPath = draft.avatarPath;
  if (draft.avatarFile) {
    onStatus("Uploading avatar…");
    const extension = (draft.avatarFile.name.split(".").pop() || "png").toLowerCase().slice(0, 5);
    const path = `public/avatar.${extension.replace(/[^a-z0-9]/g, "") || "png"}`;
    const dav = await client.dav();
    await dav.write(path, draft.avatarFile);
    avatarPath = path;
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
  // Every card that shows us should show the new name immediately.
  primeProfile(identity, saved, relayUrlFor(identity));
  return saved;
}
