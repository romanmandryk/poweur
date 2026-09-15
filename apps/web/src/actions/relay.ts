/** Plumbing every relay-reading action shares (from app.js). */
import { clientFor } from "../lib/client.js";
import { resolveProfile } from "../lib/profiles.js";
import { relayUrlFor } from "../lib/storage.js";
import { useSession } from "../state/session";

/** The active identity's client, or null while nobody is signed in. */
export function activeClient(): any {
  const identity = useSession.getState().identity;
  return identity ? clientFor(identity) : null;
}

/** Resolve a profile against the active identity's relay — what components are handed. */
export function resolveForActive(identity: string) {
  return resolveProfile(identity, relayUrlFor(useSession.getState().identity));
}

let challengeChain: Promise<unknown> = Promise.resolve();

/**
 * Run challenge-signed reads one at a time. The relay keeps one outstanding
 * challenge per identity, so whichever of two overlapping drains lands second
 * invalidates the first one's signature.
 */
export function challengeSerial<T>(task: () => Promise<T>): Promise<T> {
  const next = challengeChain.then(task, task);
  challengeChain = next.catch(() => {});
  return next;
}

export const parseMessage = (message: any) => (typeof message === "string" ? JSON.parse(message) : message);

export const messageKey = (message: any): string => message.id || `${message.sender}:${message.timestamp}`;

/** Merge by key (later wins), oldest first. Returns a new array. */
export function mergeInto<T>(store: T[], incoming: T[] | null | undefined, key: (entry: T) => string = (entry: any) => entry.id): T[] {
  const byKey = new Map(store.map((entry) => [key(entry), entry]));
  for (const entry of incoming ?? []) byKey.set(key(entry), entry);
  return [...byKey.values()].sort((a: any, b: any) => +new Date(a.timestamp) - +new Date(b.timestamp));
}

export const errorMessage = (error: unknown) => (error as Error)?.message ?? String(error);
