/**
 * What the nav badges count (E15-T13): unread messages — signed, and anonymous
 * where the inbox accepts them — on Messages, and contact requests waiting for
 * an answer on Contacts. Both come from read marks and the requests queue, so
 * they reach zero.
 */
import { markCovers, unreadTotal } from "../lib/threads.js";
import type { Contact, DataFields } from "./data";
import { validateShareClaim } from "@poweur/client";

type BadgeData = Pick<DataFields, "messages" | "anon" | "history" | "requests" | "contacts"> & Partial<Pick<DataFields, "policy">>;

export interface IncomingRequest {
  sender: string;
  timestamp: string;
  intro: string | null;
  queued: boolean;
}

export interface IncomingShareOffer {
  message: any;
  sender: string;
  shareId: string;
  path: string;
  writable: boolean;
  timestamp: string;
}

export interface IncomingShareClaim {
  message: any;
  sender: string;
  shareId: string;
  action: "viewed" | "downloaded" | "uploaded";
  timestamp: string;
}

/** Structurally valid capability-to-ID requests; the owner rechecks the live grant on approval. */
export function incomingShareClaims(data: BadgeData, identity: string | null): IncomingShareClaim[] {
  const byKey = new Map<string, IncomingShareClaim>();
  for (const raw of [...data.requests.incoming, ...data.messages]) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (message.type !== "sys.share.claim" || !message.plaintext) continue;
    if (message.expires_at && Date.parse(message.expires_at) <= Date.now()) continue;
    try {
      const claim = JSON.parse(message.plaintext);
      validateShareClaim(claim);
      const sender = String(message.sender ?? "").toLowerCase();
      if (sender !== claim.claimant.toLowerCase() || claim.owner.toLowerCase() !== String(identity ?? "").toLowerCase() ||
          String(message.metadata?.share_id ?? "") !== claim.share_id) continue;
      const candidate = { message, sender, shareId: claim.share_id, action: claim.action, timestamp: message.timestamp };
      const key = `${claim.share_id}\n${sender}`;
      const existing = byKey.get(key);
      if (!existing || Date.parse(candidate.timestamp) > Date.parse(existing.timestamp)) byKey.set(key, candidate);
    } catch { /* malformed claims are not actionable */ }
  }
  return [...byKey.values()].sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp));
}

/** Structurally plausible, unexpired offers; signature verification happens on Accept. */
export function incomingShareOffers(data: BadgeData, identity: string | null, mountedShareIds: string[] = []): IncomingShareOffer[] {
  const mounted = new Set(mountedShareIds);
  const byID = new Map<string, IncomingShareOffer>();
  for (const raw of [...data.requests.incoming, ...data.messages]) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (message.type !== "sys.share.offer" || !message.plaintext) continue;
    if (message.expires_at && Date.parse(message.expires_at) <= Date.now()) continue;
    try {
      const offer = JSON.parse(message.plaintext);
      const shareId = String(offer?.grant?.share_id ?? "");
      const sender = String(message.sender ?? "").toLowerCase();
      const audience = offer?.grant?.audience ?? [];
      if (
        offer?.version !== 1 || !shareId || mounted.has(shareId) ||
        String(offer?.grant?.owner ?? "").toLowerCase() !== sender ||
        String(message.metadata?.share_id ?? "") !== shareId ||
        !audience.some((entry: any) => String(entry.id ?? "").toLowerCase() === String(identity ?? "").toLowerCase())
      ) continue;
      const candidate = {
        message, sender, shareId, path: String(offer.grant.path ?? ""),
        writable: (offer.grant.permissions ?? []).includes("write"), timestamp: message.timestamp,
      };
      const existing = byID.get(shareId);
      if (!existing || Date.parse(candidate.timestamp) > Date.parse(existing.timestamp)) byID.set(shareId, candidate);
    } catch {
      // Malformed encrypted bodies confer no authority and are not actionable.
    }
  }
  return [...byID.values()].sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp));
}

/** The contact record for an identity, or null — the app's one lookup. */
export function contactFor(list: Contact[], identity: string | null | undefined): Contact | null {
  const wanted = String(identity ?? "").toLowerCase();
  return list.find((contact) => contact.identity.toLowerCase() === wanted) ?? null;
}

/** People asking to be a contact, newest first, one row per requester. */
export function incomingRequests(data: BadgeData, identity: string | null): IncomingRequest[] {
  const byRequester = new Map<string, IncomingRequest>();
  const add = (entry: IncomingRequest) => {
    if (contactFor(data.contacts.list, entry.sender)?.state === "blocked") return;
    const existing = byRequester.get(entry.sender);
    if (!existing || new Date(entry.timestamp) > new Date(existing.timestamp)) {
      byRequester.set(entry.sender, { ...existing, ...entry });
    }
  };
  for (const entry of data.requests.incoming) {
    // The queue also carries `sys.contact.accept` answers to requests we sent.
    if (entry.type && entry.type !== "sys.contact.request") continue;
    add({ sender: entry.sender, timestamp: entry.timestamp, intro: entry.plaintext ?? null, queued: true });
  }
  for (const raw of data.messages) {
    const message = typeof raw === "string" ? JSON.parse(raw) : raw;
    if (message.type !== "sys.contact.request" || message.sender === identity) continue;
    add({ sender: message.sender, timestamp: message.timestamp, intro: message.plaintext ?? null, queued: false });
  }
  return [...byRequester.values()].sort((a, b) => +new Date(b.timestamp) - +new Date(a.timestamp));
}

/** Whether the loaded inbox policy takes anonymous messages — the only case with a tray for them. */
export function anonymousAllowed(policy: DataFields["policy"] | undefined): boolean {
  return Boolean(policy?.doc?.anonymous?.allow);
}

export function unreadAnonymous(data: BadgeData): number {
  const mark = (data.history.readState?.conversations as Record<string, any>)?.["anonymous"] ?? null;
  return data.anon.messages.filter((message: any) => !(mark && markCovers(mark, message.timestamp, message.id ?? ""))).length;
}

export function navBadges(data: BadgeData, identity: string | null, unlocked: boolean): Partial<Record<string, number>> {
  if (!identity || !unlocked) return {};
  // With anonymous messages turned off there is no tray to clear them from.
  const anonymous = anonymousAllowed(data.policy) ? unreadAnonymous(data) : 0;
  return {
    messages: unreadTotal(data.messages, identity, data.history.readState?.conversations) + anonymous,
    contacts: incomingRequests(data, identity).length,
  };
}
