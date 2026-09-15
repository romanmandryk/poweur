import type { ProfileEntry } from "../../src/components/ProfileCard";

export const entry = (identity: string, extra: Partial<ProfileEntry> = {}): ProfileEntry => ({
  identity,
  document: { identity, public_key: "ed25519:AAA", capabilities: ["messaging"] },
  profile: null,
  displayName: null,
  bio: null,
  links: [],
  avatar: null,
  capabilities: { features: { messaging: "1" } },
  ...extra,
});

export const resolveOk = (identity: string) => Promise.resolve(entry(identity));
export const resolveFail = (): Promise<ProfileEntry> => Promise.reject(new Error("identity not found"));

/** Let queued microtasks and timers settle. */
export const settle = (ms = 0) => new Promise((resolve) => setTimeout(resolve, ms));
