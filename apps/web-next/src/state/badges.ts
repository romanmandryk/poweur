/**
 * What the nav badges count (E15-T13): unread messages — signed and anonymous
 * — on Messages, and contact requests waiting for an answer on Contacts. Both
 * come from read marks and the requests queue, so they reach zero.
 */
import { markCovers, unreadTotal } from "../lib/threads.js";
import type { Contact, DataFields } from "./data";

type BadgeData = Pick<DataFields, "messages" | "anon" | "history" | "requests" | "contacts">;

export interface IncomingRequest {
  sender: string;
  timestamp: string;
  intro: string | null;
  queued: boolean;
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

export function unreadAnonymous(data: BadgeData): number {
  const mark = (data.history.readState?.conversations as Record<string, any>)?.["anonymous"] ?? null;
  return data.anon.messages.filter((message: any) => !(mark && markCovers(mark, message.timestamp, message.id ?? ""))).length;
}

export function navBadges(data: BadgeData, identity: string | null, unlocked: boolean): Partial<Record<string, number>> {
  if (!identity || !unlocked) return {};
  return {
    messages: unreadTotal(data.messages, identity, data.history.readState?.conversations) + unreadAnonymous(data),
    contacts: incomingRequests(data, identity).length,
  };
}
