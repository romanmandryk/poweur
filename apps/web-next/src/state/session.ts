/**
 * Who is signed in on this device, and whether their keys are open.
 *
 * The keys themselves stay in `lib/storage.js` (memory + sessionStorage),
 * exactly as the legacy app holds them; `unlocked` is the React-visible
 * mirror, flipped by whoever opens or clears them.
 */
import { create } from "zustand";
import {
  clearUnlockedKeys,
  getActiveIdentity,
  getConfig,
  getUnlockedKeys,
  setActiveIdentity,
} from "../lib/storage.js";
import { clearProfileCache } from "../lib/profiles.js";
import { modeNow } from "../lib/mode.js";
import { useData } from "./data";

export interface ModeInfo {
  mode: string;
  subject?: string;
  [key: string]: unknown;
}

export interface SessionState {
  identity: string | null;
  config: Record<string, any>;
  unlocked: boolean;
  /** Which front door this host is (E15-T7); corrected when the relay answers. */
  mode: ModeInfo;
}

export const useSession = create<SessionState>()(() => ({
  identity: getActiveIdentity(),
  config: getConfig(),
  unlocked: Boolean(getUnlockedKeys()),
  mode: modeNow() as ModeInfo,
}));

/** Re-read identity, config and key state from storage. */
export function refreshSession() {
  useSession.setState({
    identity: getActiveIdentity(),
    config: getConfig(),
    unlocked: Boolean(getUnlockedKeys()),
  });
}

export function markUnlocked(unlocked = true) {
  useSession.setState({ unlocked });
}

const teardowns = new Set<() => void>();

/**
 * Register work to stop when the active identity changes — the event stream,
 * a files poller. Returns the unregister function.
 */
export function onIdentityTeardown(fn: () => void): () => void {
  teardowns.add(fn);
  return () => teardowns.delete(fn);
}

/**
 * Make `identity` active and drop everything scoped to the previous one.
 * State is keyed by identity (E15-T1), so this is a reset rather than a merge.
 */
export function switchIdentity(identity: string) {
  for (const teardown of teardowns) teardown();
  clearUnlockedKeys();
  clearProfileCache();
  setActiveIdentity(identity);
  useData.getState().resetForIdentity();
  useSession.setState({ identity, unlocked: false });
}
