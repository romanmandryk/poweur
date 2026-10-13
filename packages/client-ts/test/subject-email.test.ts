/**
 * Subjects (E09-T13) and the email bridge documents (EPIC-023) against the
 * Go vectors: message-subject.json, history-threads.json, email.json.
 */
import { describe, expect, it } from "vitest";

import {
  canonicalEmailOptIn,
  canonicalEmailUpload,
  emailAddressFor,
  emailBlobContext,
  emailConversationRoot,
  emailThreadKey,
  openEmailBlob,
  parseEmailAddress,
  sealEmailUpload,
  trustsEmailBridge,
  verifyEmailOptIn,
  EMAIL_BLOB_SEAL_DOMAIN,
  type EmailOptIn,
} from "../src/email.js";
import { generateEncryptionKeypair, openWithDomain, sealWithDomain, sha256Bytes } from "../src/crypto/index.js";
import { fromUtf8 } from "../src/encoding.js";
import { markRead, markThreadRead, parseReadState, recordThreadKey, unreadCounts, type HistoryRecord, type ReadMark } from "../src/history.js";
import { conversationKey, formatBody, newThreadId, normalizeSubject, parseBody, validateSubject } from "../src/subject.js";
import { loadVectors } from "./vectors.js";

interface SubjectVectors {
  format: { name: string; subject: string; text: string; valid: boolean; plaintext?: string; metadata?: Record<string, string> }[];
  parse: { name: string; plaintext: string; metadata?: Record<string, string>; subject: string; text: string }[];
  normalize: { subject: string; normalized: string }[];
  keys: { thread_id: string; subject: string; key: string }[];
}

describe("subjects match Go", () => {
  const v = loadVectors<SubjectVectors>("message-subject");
  it("formats", () => {
    for (const c of v.format) {
      let got: ReturnType<typeof formatBody> | null = null;
      try {
        got = formatBody(c.subject, c.text);
      } catch {
        got = null;
      }
      expect(got !== null, c.name).toBe(c.valid);
      if (got) {
        expect(got.plaintext, c.name).toBe(c.plaintext ?? "");
        expect(got.metadata ?? null, c.name).toEqual(c.metadata ?? null);
      }
    }
  });
  it("parses", () => {
    for (const c of v.parse) expect(parseBody(c.plaintext, c.metadata), c.name).toEqual({ subject: c.subject, text: c.text });
  });
  it("normalizes and keys", () => {
    for (const c of v.normalize) expect(normalizeSubject(c.subject), c.subject).toBe(c.normalized);
    for (const c of v.keys) expect(conversationKey(c.thread_id, c.subject)).toBe(c.key);
    expect(normalizeSubject("İstanbul")).toBe("istanbul");
  });
  it("mints sortable ulids", () => {
    const a = newThreadId(1);
    const b = newThreadId(2 ** 40);
    expect(a).toMatch(/^[0-9A-HJKMNP-TV-Z]{26}$/);
    expect(a.slice(0, 10) < b.slice(0, 10)).toBe(true);
    expect(newThreadId()).not.toBe(newThreadId());
    expect(() => validateSubject("a b")).toThrow();
  });
});

interface ThreadsVectors {
  owner: string;
  records: HistoryRecord[];
  thread_keys: string[];
  states: {
    name: string;
    raw: unknown;
    valid: boolean;
    unread?: Record<string, number>;
    after_thread_mark?: Record<string, Record<string, ReadMark>>;
    after_peer_mark?: Record<string, Record<string, ReadMark>>;
  }[];
}

describe("per-thread read marks match Go", () => {
  const v = loadVectors<ThreadsVectors>("history-threads");
  it("keys records", () => {
    expect(v.records.map(recordThreadKey)).toEqual(v.thread_keys);
  });
  it("counts and marks", () => {
    for (const s of v.states) {
      let state;
      try {
        state = parseReadState(JSON.stringify(s.raw));
      } catch {
        state = null;
      }
      expect(state !== null, s.name).toBe(s.valid);
      if (!state) continue;
      expect(unreadCounts(state, v.owner, v.records), s.name).toEqual(s.unread ?? {});
      expect(markThreadRead(state, "bob.example.org", "01J9ZB", "2026-02-01T09:05:00Z", "t2").threads ?? null, s.name).toEqual(s.after_thread_mark ?? null);
      expect(markRead(state, "bob.example.org", "2026-02-01T09:10:00Z", "t3").threads ?? null, s.name).toEqual(s.after_peer_mark ?? null);
    }
  });
});

interface EmailVectors {
  public_key: string;
  optin: EmailOptIn;
  optin_canonical: string;
  addresses: { input: string; valid: boolean; local?: string; tag?: string; domain?: string }[];
  identities: { identity: string; address?: string; valid: boolean }[];
  upload_canonical: string;
  blob_context: string;
  roots: { message_id: string; in_reply_to: string; references: string[] | null; root: string; thread_key: string }[];
}

describe("email documents match Go", () => {
  const v = loadVectors<EmailVectors>("email");
  it("opt-in canonical and signature", () => {
    expect(canonicalEmailOptIn(v.optin)).toBe(v.optin_canonical);
    expect(verifyEmailOptIn(v.optin, v.public_key)).toBe(true);
    expect(verifyEmailOptIn({ ...v.optin, bridge: "evil.example.org" }, v.public_key)).toBe(false);
    expect(verifyEmailOptIn({ ...v.optin, address: "boss@poweur.net" }, v.public_key)).toBe(false);
  });
  it("addresses", () => {
    for (const c of v.addresses) {
      let route = null;
      try {
        route = parseEmailAddress(c.input);
      } catch {
        route = null;
      }
      expect(route !== null, c.input).toBe(c.valid);
      if (route) expect(route, c.input).toEqual({ local: c.local ?? "", tag: c.tag ?? "", domain: c.domain ?? "" });
    }
    for (const c of v.identities) {
      let address = "";
      try {
        address = emailAddressFor(c.identity);
      } catch {
        address = "";
      }
      expect(Boolean(address), c.identity).toBe(c.valid);
      if (c.valid) expect(address).toBe(c.address);
    }
  });
  it("upload canonical, blob context, conversation root", () => {
    expect(canonicalEmailUpload("John.poweur.net", "01J9ZUPLOAD", "ABCDEF", 1234, "2026-10-13T12:00:00Z", "n1")).toBe(v.upload_canonical);
    expect(fromUtf8(emailBlobContext("01J9ZBLOB"))).toBe(v.blob_context);
    for (const c of v.roots) {
      expect(emailConversationRoot(c.message_id, c.in_reply_to, c.references)).toBe(c.root);
      expect(emailThreadKey({ message_id: c.message_id, in_reply_to: c.in_reply_to, references: c.references ?? [] }, "rec1")).toBe(c.thread_key);
    }
  });
  it("trusts only the named bridge", () => {
    const policy = { version: 1, mode: "contacts_only" as const, email: { allow: true, bridge: "email.poweur.net" } };
    expect(trustsEmailBridge(policy, "Email.poweur.net")).toBe(true);
    expect(trustsEmailBridge(policy, "mallory.poweur.net")).toBe(false);
    expect(trustsEmailBridge({ ...policy, email: { allow: false, bridge: "email.poweur.net" } }, "email.poweur.net")).toBe(false);
    expect(trustsEmailBridge({ version: 1, mode: "open" }, "email.poweur.net")).toBe(false);
  });
});

describe("email blobs", () => {
  it("opens a sealed blob and checks its hash", async () => {
    const keys = generateEncryptionKeypair();
    const plain = new TextEncoder().encode("big attachment");
    const sealed = sealWithDomain(keys.publicKey, plain, EMAIL_BLOB_SEAL_DOMAIN, emailBlobContext("b1"));
    const body = JSON.stringify({ ciphertext: sealed.ciphertext, ephemeral_public_key: sealed.ephemeralPublicKey, nonce: sealed.nonce });
    const fetchImpl = (async () => new Response(body, { status: 200 })) as unknown as typeof fetch;
    const decryptor = { identity: "a.example.org", encryptionPublicKey: keys.publicKey, privateKeyBytes: async () => keys.privateKey } as any;
    const sha = Array.from(sha256Bytes(plain), (b) => b.toString(16).padStart(2, "0")).join("");
    const out = await openEmailBlob(decryptor, { id: "b1", url: "https://bridge.example/blobs/b1", size: plain.length, sha256: sha }, fetchImpl);
    expect(new TextDecoder().decode(out)).toBe("big attachment");
    await expect(openEmailBlob(decryptor, { id: "b2", url: "https://bridge.example/blobs/b1", size: 1, sha256: sha }, fetchImpl)).rejects.toThrow();
    await expect(openEmailBlob(decryptor, { id: "b1", url: "https://bridge.example/blobs/b1", size: 1, sha256: "00" }, fetchImpl)).rejects.toThrow();
  });
  it("seals uploads to the bridge with the upload id bound", () => {
    const keys = generateEncryptionKeypair();
    const wire = JSON.parse(sealEmailUpload(keys.publicKey, new Uint8Array([1, 2, 3]), "u1"));
    const opened = openWithDomain(keys.privateKey, { ciphertext: wire.ciphertext, ephemeralPublicKey: wire.ephemeral_public_key, nonce: wire.nonce }, "poweur/email/upload/v1", emailBlobContext("u1"));
    expect(Array.from(opened)).toEqual([1, 2, 3]);
  });
});
