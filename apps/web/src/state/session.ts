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

/** `lib/mode.js`'s ModeInfo: which front door this host is. */
export interface ModeInfo {
  mode: string;
  host?: string;
  subject?: string;
  handle?: string;
  domain?: string;
  hostedDomains?: string[];
  launcherHosts?: string[];
  launcherHost?: string;
  resolved?: boolean;
  probed?: boolean;
  reachable?: boolean;
}

export interface SessionState {
  identity: string | null;
  config: Record<string, any>;
  unlocked: boolean;
  /** Which front door this host is (E15-T7); corrected when the relay answers. */
  mode: ModeInfo;
  /**
   * Bumped when something outside the store changed what screens read from
   * storage — a rotated key, a revoked session — so they repaint.
   */
  revision: number;
}

export const useSession = create<SessionState>()(() => ({
  identity: getActiveIdentity(),
  config: getConfig(),
  unlocked: Boolean(getUnlockedKeys()),
  mode: modeNow() as ModeInfo,
  revision: 0,
}));

/** Storage changed under the screens (identity record, session record): repaint them. */
export function touchSession() {
  useSession.setState((state) => ({ revision: state.revision + 1, config: getConfig() }));
}

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

const unlockHooks = new Set<() => void>();

/**
 * Register work to start once keys are open — the inbox, requests and
 * contacts loads messaging owns (E21-T7). Returns the unregister function.
 */
export function onUnlocked(fn: () => void): () => void {
  unlockHooks.add(fn);
  return () => unlockHooks.delete(fn);
}

/** Legacy `pullAfterUnlock()`: flip the mirror, then run every hook. */
export function afterUnlock() {
  useSession.setState({ unlocked: true });
  for (const hook of unlockHooks) hook();
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
export function switchIdentity(identity: string | null) {
  for (const teardown of teardowns) teardown();
  clearUnlockedKeys();
  clearProfileCache();
  setActiveIdentity(identity);
  useData.getState().resetForIdentity();
  useSession.setState({ identity, unlocked: false });
}
