/** Addressable group messaging (EPIC-009 E09-T5). */

import { canonicalMessage, canonicalShareGroup } from "./canonical.js";
import { encryptMessage, parseEd25519PublicKey, verifyCanonical } from "./crypto/index.js";
import type { Signer } from "./crypto/keys.js";
import { rfc3339, stripKeyPrefix } from "./encoding.js";
import { PoweurError } from "./errors.js";
import { RelayClient } from "./http.js";
import { newMessageId } from "./ids.js";
import { validateEnvelopeExtensions, validateThreadId } from "./msgtypes.js";
import { resolveIdentity, type ResolveOptions } from "./resolve.js";
import type { GroupFanoutResponse, Message, ShareGroup } from "./types.js";

export const MAX_GROUP_FANOUT_MEMBERS = 100;

export interface GroupMessagingOptions {
  client: RelayClient;
  resolve?: ResolveOptions;
  clientFor?: (relayUrl: string) => RelayClient;
}

export interface GroupSendOptions {
  type?: string;
  thread?: string;
  expiresAt?: string;
  metadata?: Record<string, string>;
}

export interface GroupSendResult {
  group: ShareGroup;
  envelopes: Message[];
  response: GroupFanoutResponse;
  targetRelay: string;
}

export function groupThreadId(group: string, suffix = ""): string {
  const name = group.trim().toLowerCase();
  const part = suffix.trim().toLowerCase();
  if (!part || part === name) return name;
  return part.startsWith(`${name}:`) ? part : `${name}:${part}`;
}

export function validateGroupThreadId(group: string, threadId: string): string | null {
  const name = group.trim().toLowerCase();
  const value = threadId.trim().toLowerCase();
  const baseError = validateThreadId(value);
  if (baseError) return baseError;
  if (value === name || (value.startsWith(`${name}:`) && value.length > name.length + 1)) {
    return null;
  }
  return `group thread_id must be ${name} or start with ${name}:`;
}

export class GroupMessaging {
  readonly #options: GroupMessagingOptions;

  constructor(options: GroupMessagingOptions) {
    this.#options = options;
  }

  #scheme(): "http" | "https" {
    return this.#options.client.relayUrl.startsWith("http://") ? "http" : "https";
  }

  #clientFor(relayUrl: string): RelayClient {
    if (relayUrl === this.#options.client.relayUrl) return this.#options.client;
    return this.#options.clientFor?.(relayUrl) ?? new RelayClient(relayUrl, {
      ...(this.#options.resolve?.fetch ? { fetch: this.#options.resolve.fetch } : {}),
    });
  }

  async roster(signer: Signer, group: string): Promise<{ document: ShareGroup; relay: RelayClient; relayUrl: string }> {
    const name = group.trim().toLowerCase();
    const resolved = await resolveIdentity(name, this.#options.resolve ?? {});
    const relayValue = resolved.document.relay || name;
    const relayUrl = /^https?:\/\//.test(relayValue)
      ? relayValue.replace(/\/+$/, "")
      : `${this.#scheme()}://${relayValue.replace(/\/+$/, "")}`;
    const relay = this.#clientFor(relayUrl);
    const document = await relay.request<ShareGroup>({
      method: "GET",
      path: `/groups/${encodeURIComponent(name)}`,
      sign: { signer },
    });
    if (document.group.toLowerCase() !== name || document.owner.toLowerCase() !== name) {
      throw new PoweurError("invalid_document", "relay returned a document for another group");
    }
    if (!verifyGroupSignature(document, resolved.document.public_key)) {
      throw new PoweurError("invalid_signature", "group membership document signature is invalid");
    }
    return { document, relay, relayUrl };
  }

  async send(signer: Signer, group: string, plaintext: string, options: GroupSendOptions = {}): Promise<GroupSendResult> {
    if (options.metadata?.group !== undefined || options.metadata?.epoch !== undefined) {
      throw new PoweurError("invalid_argument", "group and epoch metadata are reserved");
    }
    const { document, relay, relayUrl } = await this.roster(signer, group);
    const sender = signer.identity.trim().toLowerCase();
    const members = [...new Set(document.members.map((member) => member.trim().toLowerCase()))].sort();
    if (!members.includes(sender)) throw new PoweurError("policy_rejected", `${sender} is not a member of ${group}`);
    if (members.length > MAX_GROUP_FANOUT_MEMBERS) {
      throw new PoweurError("unsupported", `v1 fan-out is limited to ${MAX_GROUP_FANOUT_MEMBERS} members`);
    }
    const recipients = members.filter((member) => member !== sender);
    if (!recipients.length) throw new PoweurError("invalid_argument", `${group} has no other members`);
    const threadId = groupThreadId(document.group, options.thread ?? "");
    const threadError = validateGroupThreadId(document.group, threadId);
    if (threadError) throw new PoweurError("invalid_argument", threadError);
    const metadata = { ...(options.metadata ?? {}), group: document.group.toLowerCase(), epoch: String(document.epoch ?? 0) };
    const extensionError = validateEnvelopeExtensions({
      type: options.type,
      threadId,
      expiresAt: options.expiresAt,
      metadata,
    });
    if (extensionError) throw new PoweurError("invalid_argument", extensionError);
    const timestamp = rfc3339();
    const envelopes: Message[] = [];
    for (const recipient of recipients) {
      const target = await resolveIdentity(recipient, this.#options.resolve ?? {});
      const encryptionKey = target.document.encryption_public_key;
      if (!encryptionKey) {
        throw new PoweurError("not_found", `${recipient} has no published encryption key`);
      }
      const encrypted = encryptMessage(stripKeyPrefix(encryptionKey), plaintext);
      const message: Message = {
        id: newMessageId(), sender, recipient, timestamp,
        payload: encrypted.payload, encryption: encrypted.encryption,
        signature: "", thread_id: threadId, metadata,
        ...(options.type ? { type: options.type } : {}),
        ...(options.expiresAt ? { expires_at: options.expiresAt } : {}),
      };
      message.signature = await signer.sign(canonicalMessage({
        sender: message.sender, recipient: message.recipient, timestamp: message.timestamp,
        payload: message.payload, id: message.id, encryption: message.encryption,
        ...(message.type ? { type: message.type } : {}), threadId,
        ...(message.expires_at ? { expiresAt: message.expires_at } : {}), metadata,
      }), "base64std");
      envelopes.push(message);
    }
    const response = await relay.request<GroupFanoutResponse>({
      method: "POST",
      path: `/groups/${encodeURIComponent(document.group)}/messages`,
      body: { group: document.group, epoch: document.epoch ?? 0, envelopes },
    });
    return { group: document, envelopes, response, targetRelay: relayUrl };
  }
}

/** Verify a group document's signature against its owner's key. */
export function verifyGroupSignature(group: ShareGroup, ownerPublicKey: string): boolean {
  return verifyCanonical(
    parseEd25519PublicKey(ownerPublicKey),
    canonicalShareGroup(group),
    group.signature,
  );
}
