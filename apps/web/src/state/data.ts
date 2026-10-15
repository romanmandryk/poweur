/**
 * The app's data, per identity — the legacy `S` object minus what moved to the
 * session store (identity, config) and the UI store (dropdown). Screens own
 * the actions that fill these slices (E21-T6…T11); this file owns their shape
 * and the reset that switching identity performs.
 */
import { emailBridgeOf as sdkEmailBridgeOf } from "@poweur/client";
import { create } from "zustand";
import type { DriveFiles, Mount, OpenFile } from "@poweur/client/drive";

export type Tray = "inbox" | "requests" | "anonymous" | "email";
/** How the Inbox tray groups (E09-T13): one row per ID, or per subject. */
export type TrayView = "id" | "subject";
export type FilesTab = "mine" | "shared";

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
export const freshEmail = () => ({
  enabled: false,
  address: null as string | null,
  bridge: null as string | null,
  /** The bridge the relay advertises, if any. */
  offered: null as string | null,
  /** False when the bridge only receives mail (no composing). */
  canSend: true,
  loading: false,
  loaded: false,
  error: null as string | null,
});
export const freshProfile = () => ({ doc: null as any, explicit: false, loaded: false, loading: false });
export const freshFiles = () => ({
  identity: null as string | null,
  own: null as { files: DriveFiles; root: OpenFile } | null,
  folders: {} as Record<string, { folder: OpenFile; entries: OpenFile[] }>,
  mounts: [] as Mount[],
  /** The root listing this device saw last, shown until the drive opens. */
  preview: null as FilesPreviewEntry[] | null,
  /** The drive's changes cursor as of the last full refresh. */
  cursor: null as string | null,
  /** Bytes used and the quota (0: no limit), as of the last drive read. */
  usage: null as { used: number; quota: number } | null,
  loading: false,
  loaded: false,
  error: null as string | null,
});
export interface FilesPreviewEntry { node: string; name: string; kind: "file" | "folder" }

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
    email: freshEmail(),
    files: freshFiles(),
    /** The open conversation (E15-T13), or null. */
    thread: null as any,
  };
}

export function freshData() {
  return {
    ...freshIdentityData(),
    tray: "inbox" as Tray,
    /** Per device and identity, from localStorage (lib/trayView). */
    trayView: "id" as TrayView,
    /** The open mail thread, or null. */
    mailThread: null as string | null,
    /** The Files tab, kept across navigation like the message tray. */
    filesTab: "mine" as FilesTab,
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
    /** A tapped sign-in notification: open the newest prompt once it is in (EPIC-028). */
    openNewestPrompt: false,
    /** A pairing link opened on this device (#pair=…), waiting to be approved. */
    pairInput: "",
    auth: {
      input: "",
      request: null as any,
      metadata: null as any,
      headline: "",
      context: "",
      /**
       * Approved on another device than the one that started it (a QR, a link
       * by reference, a pushed prompt, the shell): it finishes on that page's
       * poll, not here (EPIC-028).
       */
      crossDevice: false,
      /** Set when the sign-in was pushed: pick the number the starting screen shows. */
      choices: null as string[] | null,
      /** The RP's "push this to my app" endpoint, for a browser without the keys. */
      pushUri: "",
      /** The approve page came back from an Android intent: the app is not installed. */
      noApp: false,
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

/**
 * The email bridge whose mail the user takes (EPIC-023), or "": the one the
 * inbox policy names, else — email is on by default — the one the relay
 * offers, unless the policy opts out. Use as a selector with both values.
 */
export function emailBridgeOf(policy: any, offered: string | null | undefined): string {
  return sdkEmailBridgeOf(policy, offered);
}
