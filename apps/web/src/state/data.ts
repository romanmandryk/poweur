/**
 * The app's data, per identity — the legacy `S` object minus what moved to the
 * session store (identity, config) and the UI store (dropdown). Screens own
 * the actions that fill these slices (E21-T6…T11); this file owns their shape
 * and the reset that switching identity performs.
 */
import { create } from "zustand";

export type Tray = "inbox" | "requests" | "anonymous";

export interface Contact {
  identity: string;
  petname?: string | null;
  state?: "accepted" | "requested" | "blocked" | string;
  [key: string]: unknown;
}

export interface LoadState {
  loading: boolean;
  loaded: boolean;
  error: string | null;
}

export const freshContacts = () => ({ list: [] as Contact[], loading: false, loaded: false, error: null as string | null, filter: "" });
export const freshHistory = () => ({
  loading: false,
  loaded: false,
  error: null as string | null,
  readState: { conversations: {} as Record<string, { timestamp: string; id?: string }> },
});
export const freshRequests = () => ({ incoming: [] as any[], loading: false, loaded: false, error: null as string | null, fetchedAt: 0 });
export const freshAnon = () => ({ messages: [] as any[], loading: false, loaded: false, error: null as string | null, fetchedAt: 0 });
export const freshPolicy = () => ({ doc: null as any, explicit: false, loading: false, loaded: false });
export const freshProfile = () => ({ doc: null as any, explicit: false, loaded: false, loading: false });

/** Everything that belongs to whoever is signed in. */
function freshIdentityData() {
  return {
    messages: [] as any[],
    acks: [] as any[],
    contacts: freshContacts(),
    history: freshHistory(),
    requests: freshRequests(),
    anon: freshAnon(),
    policy: freshPolicy(),
    profile: freshProfile(),
    /** The open conversation (E15-T13), or null. */
    thread: null as any,
  };
}

export function freshData() {
  return {
    ...freshIdentityData(),
    tray: "inbox" as Tray,
    /** null unless the first-run flow is on screen. */
    onboard: null as any,
    /** The identity host's own name (E15-T9). */
    door: { subject: "", state: "idle", message: "", policy: null as any },
    /** `checkPasskeySupport()`, resolved once. */
    passkey: null as any,
    /** `chooseCustody()`, likewise. */
    custody: null as any,
    /** Sign-in prompts the user dismissed this session. */
    dismissedPrompts: [] as string[],
    /** A pairing link opened on this device (#pair=…), waiting to be approved. */
    pairInput: "",
    auth: {
      input: "",
      request: null as any,
      metadata: null as any,
      headline: "",
      context: "",
      /** Opened from a sign-in prompt: the page that started it is elsewhere. */
      requireCode: false,
      scopes: [] as any[],
      loading: false,
      error: "",
      result: null as any,
    },
  };
}

export type DataFields = ReturnType<typeof freshData>;

export interface DataState extends DataFields {
  /** Drop everything scoped to the previous identity (legacy `switchIdentity`). */
  resetForIdentity(): void;
}

export const useData = create<DataState>()((set) => ({
  ...freshData(),
  resetForIdentity: () => set(freshIdentityData()),
}));
