/**
 * `~/.poweur/pending/<identity>.jsonl` — the local delivery journal, shared
 * with the Go CLI so `poweur messages status` renders ticks written by either
 * client.
 *
 * Append-only JSON Lines, one record per state transition; reads collapse the
 * log into the latest state per message id. Every write is a single short
 * append, so concurrent CLI invocations don't corrupt each other.
 */

import { appendFileSync, existsSync, mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { journalDir } from "./paths.js";

export const STATE_QUEUED = "queued";
export const STATE_DELIVERED_HOME_RELAY = "delivered_home_relay";
export const STATE_DELIVERED_RECIPIENT_RELAY = "delivered_recipient_relay";
export const STATE_DELIVERED_CLIENT = "delivered_client";
export const STATE_READ = "read";
export const STATE_FAILED = "failed";

export type JournalState =
  | typeof STATE_QUEUED
  | typeof STATE_DELIVERED_HOME_RELAY
  | typeof STATE_DELIVERED_RECIPIENT_RELAY
  | typeof STATE_DELIVERED_CLIENT
  | typeof STATE_READ
  | typeof STATE_FAILED;

export interface JournalEntry {
  message_id: string;
  sender: string;
  recipient: string;
  timestamp: string;
  state: JournalState;
  detail?: string;
  via_home_relay?: boolean;
}

export interface JournalStatus {
  message_id: string;
  sender: string;
  recipient: string;
  state: JournalState;
  updated_at: string;
  via_home_relay?: boolean;
  history: JournalEntry[];
}

/**
 * Ordering for the "latest wins" collapse. `failed` ranks highest so it
 * sticks once recorded — a message that failed did not later succeed.
 */
function stateRank(state: JournalState): number {
  switch (state) {
    case STATE_QUEUED: return 1;
    case STATE_DELIVERED_HOME_RELAY: return 2;
    case STATE_DELIVERED_RECIPIENT_RELAY: return 3;
    case STATE_DELIVERED_CLIENT: return 4;
    case STATE_READ: return 5;
    case STATE_FAILED: return 6;
    default: return 0;
  }
}

function journalPath(identity: string, dir = journalDir()): string {
  return join(dir, `${identity}.jsonl`);
}

export function appendJournal(entry: JournalEntry, dir = journalDir()): void {
  if (!entry.message_id) throw new Error("journal: message_id required");
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  appendFileSync(journalPath(entry.sender, dir), `${JSON.stringify(entry)}\n`, { mode: 0o600 });
}

export function loadJournal(identity: string, dir = journalDir()): JournalEntry[] {
  const path = journalPath(identity, dir);
  if (!existsSync(path)) return [];
  return readFileSync(path, "utf8")
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => JSON.parse(line) as JournalEntry);
}

/** Collapse the journal into one status per message id, first-seen order. */
export function journalStatuses(identity: string, dir = journalDir()): JournalStatus[] {
  const index = new Map<string, number>();
  const out: JournalStatus[] = [];
  for (const entry of loadJournal(identity, dir)) {
    const position = index.get(entry.message_id);
    if (position !== undefined) {
      const status = out[position] as JournalStatus;
      status.history.push(entry);
      if (entry.state && stateRank(entry.state) >= stateRank(status.state)) {
        status.state = entry.state;
        status.updated_at = entry.timestamp;
      }
      if (entry.via_home_relay) status.via_home_relay = true;
      continue;
    }
    index.set(entry.message_id, out.length);
    out.push({
      message_id: entry.message_id,
      sender: entry.sender,
      recipient: entry.recipient,
      state: entry.state,
      updated_at: entry.timestamp,
      ...(entry.via_home_relay ? { via_home_relay: true } : {}),
      history: [entry],
    });
  }
  return out;
}

/** Compact terminal indicator: ✓ relay, ✓✓ client, ✓✓✓ read. */
export function tickGlyph(state: JournalState): string {
  switch (state) {
    case STATE_QUEUED: return " · ";
    case STATE_DELIVERED_HOME_RELAY:
    case STATE_DELIVERED_RECIPIENT_RELAY: return " ✓ ";
    case STATE_DELIVERED_CLIENT: return " ✓✓";
    case STATE_READ: return "✓✓✓";
    case STATE_FAILED: return " ✗ ";
    default: return "   ";
  }
}
