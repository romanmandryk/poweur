/**
 * Contacts and key pinning — the twin of `poweur contacts` / `poweur requests`.
 *
 * contacts.json lives in the owner's tree at `poweur-sys/relay/contacts.json`,
 * so it syncs across devices like any file and the relay can read it to
 * enforce the inbox policy. Keys are pinned at accept time (TOFU): a contact
 * whose resolved key later changes without a signed rotation is refused, the
 * known-hosts model.
 */

import { parseEd25519PublicKey } from "./crypto/index.js";
import type { Signer } from "./crypto/keys.js";
import { rfc3339, stripKeyPrefix, toBase64url, withEd25519Prefix } from "./encoding.js";
import { PoweurError } from "./errors.js";
import type { DavClient } from "./files.js";
import type { RelayClient } from "./http.js";
import { resolveIdentity, type ResolveOptions } from "./resolve.js";
import {
  CONTACT_ACCEPTED,
  CONTACT_BLOCKED,
  CONTACT_REQUESTED,
  type Contact,
  type ContactRequestEntry,
  type ContactState,
  type ContactsFile,
} from "./types.js";

export const CONTACTS_PATH = "poweur-sys/relay/contacts.json";
export const MAX_CONTACTS = 10_000;

export function emptyContactsFile(): ContactsFile {
  return { version: 1, contacts: [] };
}

/** Structural validation mirroring `ContactsFile.Validate`. */
export function validateContactsFile(file: ContactsFile): void {
  if (file.version !== 0 && file.version !== 1) {
    throw new PoweurError("invalid_document", `unsupported contacts version ${file.version}`);
  }
  if (file.contacts.length > MAX_CONTACTS) {
    throw new PoweurError("invalid_document", `contacts exceed ${MAX_CONTACTS} entries`);
  }
  const seen = new Set<string>();
  file.contacts.forEach((contact, index) => {
    const id = contact.identity?.trim().toLowerCase();
    if (!id) throw new PoweurError("invalid_document", `contact ${index}: identity is required`);
    if (seen.has(id)) throw new PoweurError("invalid_document", `duplicate contact "${id}"`);
    seen.add(id);
    if (![CONTACT_REQUESTED, CONTACT_ACCEPTED, CONTACT_BLOCKED].includes(contact.state)) {
      throw new PoweurError(
        "invalid_document",
        `contact "${id}": invalid state "${contact.state}"`,
      );
    }
    if (contact.pinned_key) parseEd25519PublicKey(contact.pinned_key);
  });
}

export function findContact(file: ContactsFile, identity: string): Contact | null {
  const wanted = identity.trim().toLowerCase();
  return file.contacts.find((c) => c.identity.trim().toLowerCase() === wanted) ?? null;
}

/** Replace or append an entry, returning a new file (no mutation). */
export function upsertContact(file: ContactsFile, contact: Contact): ContactsFile {
  const contacts = [...file.contacts];
  const index = contacts.findIndex(
    (c) => c.identity.trim().toLowerCase() === contact.identity.trim().toLowerCase(),
  );
  if (index >= 0) contacts[index] = contact;
  else contacts.push(contact);
  return { version: file.version || 1, contacts };
}

export function removeContact(file: ContactsFile, identity: string): { file: ContactsFile; removed: boolean } {
  const wanted = identity.trim().toLowerCase();
  const contacts = file.contacts.filter((c) => c.identity.trim().toLowerCase() !== wanted);
  return {
    file: { version: file.version || 1, contacts },
    removed: contacts.length !== file.contacts.length,
  };
}

export interface PinCheckResult {
  status: "ok" | "unpinned" | "rotated" | "mismatch";
  pinnedKey?: string;
  resolvedKey?: string;
}

export class Contacts {
  readonly #dav: DavClient;
  readonly #resolve: ResolveOptions;

  constructor(dav: DavClient, resolveOptions: ResolveOptions = {}) {
    this.#dav = dav;
    this.#resolve = resolveOptions;
  }

  async load(): Promise<ContactsFile> {
    const raw = await this.#dav.readOptional(CONTACTS_PATH);
    if (!raw) return emptyContactsFile();
    const file = JSON.parse(raw) as ContactsFile;
    validateContactsFile(file);
    return file;
  }

  async save(file: ContactsFile): Promise<void> {
    validateContactsFile(file);
    await this.#dav.writeJson(CONTACTS_PATH, file);
  }

  /** The contact's current signing key, in `ed25519:` form, for pinning. */
  async resolvePin(identity: string): Promise<string> {
    const { document } = await resolveIdentity(identity, this.#resolve);
    return withEd25519Prefix(toBase64url(parseEd25519PublicKey(document.public_key)));
  }

  /**
   * Write a contact in the given state. `accepted` pins the currently
   * resolved key; existing petnames and pins survive a re-write.
   *
   * `pin` forces the same TOFU pin for a non-accepted state — what an
   * outbound request does, so the key you addressed is the key you keep.
   */
  async set(
    identity: string,
    state: ContactState,
    options: { petname?: string; pin?: boolean } = {},
  ): Promise<Contact> {
    const target = identity.trim().toLowerCase();
    const file = await this.load();
    const existing = findContact(file, target);
    const entry: Contact = {
      identity: target,
      state,
      added_at: existing?.added_at ?? rfc3339(),
    };
    if (state === CONTACT_ACCEPTED || options.pin) {
      entry.pinned_key = await this.resolvePin(target);
    } else if (existing?.pinned_key) {
      entry.pinned_key = existing.pinned_key;
    }
    const petname = options.petname || existing?.petname;
    if (petname) entry.petname = petname;
    await this.save(upsertContact(file, entry));
    return entry;
  }

  async remove(identity: string): Promise<boolean> {
    const { file, removed } = removeContact(await this.load(), identity);
    if (removed) await this.save(file);
    return removed;
  }

  /**
   * Check a recipient against their pin before sending.
   *
   * `rotated` means the pinned key appears in the resolved document's
   * `previous_keys`, i.e. a legitimate rotation the caller should re-pin.
   * `mismatch` is the dangerous case: a changed key with no rotation
   * statement, which is what a compromised relay or registrar looks like.
   */
  async checkPin(recipient: string): Promise<PinCheckResult> {
    const file = await this.load().catch(() => emptyContactsFile());
    const contact = findContact(file, recipient);
    if (!contact?.pinned_key) return { status: "unpinned" };

    const { document } = await resolveIdentity(recipient, this.#resolve);
    const pinned = stripKeyPrefix(contact.pinned_key);
    const resolved = stripKeyPrefix(document.public_key);
    if (pinned === resolved) {
      return { status: "ok", pinnedKey: contact.pinned_key, resolvedKey: document.public_key };
    }
    for (const previous of document.previous_keys ?? []) {
      if (stripKeyPrefix(previous.public_key) === pinned) {
        return {
          status: "rotated",
          pinnedKey: contact.pinned_key,
          resolvedKey: document.public_key,
        };
      }
    }
    return {
      status: "mismatch",
      pinnedKey: contact.pinned_key,
      resolvedKey: document.public_key,
    };
  }

  /** Move a contact's pin to a new key (after a rotation, or on instruction). */
  async repin(identity: string, newKey: string): Promise<void> {
    const file = await this.load();
    const contact = findContact(file, identity);
    if (!contact) return;
    await this.save(
      upsertContact(file, {
        ...contact,
        pinned_key: withEd25519Prefix(toBase64url(parseEd25519PublicKey(newKey))),
      }),
    );
  }
}

/**
 * Drain the pending contact-request queue. Challenge-signed with the identity
 * key, the same shape as the inbox fetch.
 */
export async function fetchRequests(
  client: RelayClient,
  signer: Signer,
): Promise<ContactRequestEntry[]> {
  const { challenge } = await client.request<{ challenge: string }>({
    method: "GET",
    path: `/auth/challenge?identity=${encodeURIComponent(signer.identity)}`,
  });
  const signature = await signer.sign(challenge, "base64std");
  const response = await client.request<{ requests?: ContactRequestEntry[] }>({
    method: "GET",
    path: `/requests/${encodeURIComponent(signer.identity)}`,
    headers: {
      "X-Poweur-Identity": signer.identity,
      "X-Poweur-Challenge": challenge,
      "X-Poweur-Signature": signature,
    },
  });
  return response.requests ?? [];
}
