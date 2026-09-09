/**
 * Go ↔ TypeScript conformance (EPIC-017 E17-T5).
 *
 * Every assertion here re-derives a value from the TypeScript implementation
 * and compares it against a fixture the Go implementation produced. Go stays
 * canonical: if a canonical string, document shape or crypto parameter
 * changes in Go and not here, this file fails.
 */

import { describe, expect, it } from "vitest";

import {
  canonicalAck,
  canonicalDavToken,
  canonicalEncryptionKeyUpdate,
  canonicalIdentityExport,
  canonicalIdentityRegistration,
  canonicalIdentityRotation,
  canonicalKeystoreEnroll,
  canonicalKeystoreList,
  canonicalKeystoreRemove,
  canonicalMessage,
  canonicalSessionRegistration,
  canonicalSessionRevocation,
  canonicalShareGrant,
  canonicalShareGroup,
  normalizeGrantPath,
} from "../src/canonical.js";
import { validateContactsFile } from "../src/contacts.js";
import {
  decryptMessage,
  parseEd25519PublicKey,
  signCanonical,
  verifyCanonical,
} from "../src/crypto/index.js";
import { canonicalDocument, keyValidAt, verifyDocument } from "../src/document.js";
import { fromBase64, fromUtf8, toBase64url } from "../src/encoding.js";
import {
  encryptionKeyFingerprint,
  fingerprintOrKey,
  keyFingerprint,
} from "../src/fingerprint.js";
import {
  isValidIdentityName,
  sanitizeIdentityDirName,
  validateHostedHandle,
} from "../src/names.js";
import { validateInboxPolicy } from "../src/policy.js";
import {
  historyFileName,
  historyPath,
  markRead,
  parseReadState,
  unreadCounts,
  type HistoryRecord,
} from "../src/history.js";
import { validateProfile } from "../src/profile.js";
import { checkPow, clampPowBits } from "../src/pow.js";
import { verifyGrantSignature, verifyGroupSignature } from "../src/shares.js";
import type {
  ContactsFile,
  EncryptionMeta,
  IdentityDocument,
  InboxPolicy,
  Profile,
  ShareGrant,
  ShareGroup,
} from "../src/types.js";
import { loadVectors } from "./vectors.js";

interface CanonicalVector {
  name: string;
  inputs: Record<string, unknown>;
  canonical: string;
  signature: string;
}

interface CanonicalFile {
  seed_base64url: string;
  public_key_base64url: string;
  vectors: CanonicalVector[];
}

const string = (inputs: Record<string, unknown>, key: string): string =>
  (inputs[key] as string | undefined) ?? "";

describe("canonical signing strings match Go", () => {
  const file = loadVectors<CanonicalFile>("canonical");
  const seed = fromBase64(file.seed_base64url);
  const publicKey = parseEd25519PublicKey(file.public_key_base64url);

  /** Re-derive a canonical string from the same inputs Go used. */
  function derive(vector: CanonicalVector): string {
    const i = vector.inputs;
    switch (vector.name) {
      case "message-minimal":
      case "message-session-encrypted":
      case "message-typed":
        return canonicalMessage({
          sender: string(i, "sender"),
          recipient: string(i, "recipient"),
          timestamp: string(i, "timestamp"),
          payload: string(i, "payload"),
          id: string(i, "id"),
          sessionId: string(i, "session_id"),
          encryption: (i["encryption"] as EncryptionMeta | undefined) ?? null,
          type: string(i, "type"),
        });
      case "ack":
      case "ack-session":
        return canonicalAck({
          id: string(i, "id"),
          messageId: string(i, "message_id"),
          state: string(i, "state"),
          sender: string(i, "sender"),
          recipient: string(i, "recipient"),
          timestamp: string(i, "timestamp"),
          sessionId: string(i, "session_id"),
        });
      case "identity-registration":
        return canonicalIdentityRegistration(
          string(i, "identity"), string(i, "public_key"),
          string(i, "encryption_public_key"), string(i, "relay_address"),
          string(i, "issued_at"), string(i, "nonce"),
        );
      case "identity-export":
        return canonicalIdentityExport(string(i, "identity"), string(i, "issued_at"), string(i, "nonce"));
      case "identity-rotation":
        return canonicalIdentityRotation(
          string(i, "identity"), string(i, "old_public_key"), string(i, "new_public_key"),
          string(i, "issued_at"), string(i, "nonce"),
        );
      case "identity-encryption-key":
        return canonicalEncryptionKeyUpdate(
          string(i, "identity"), string(i, "encryption_public_key"),
          string(i, "issued_at"), string(i, "nonce"),
        );
      case "dav-token":
        return canonicalDavToken(
          string(i, "identity"), string(i, "audience"), string(i, "scope"),
          string(i, "issued_at"), string(i, "nonce"),
        );
      case "keystore-enroll":
      case "keystore-enroll-no-credential":
        return canonicalKeystoreEnroll(
          string(i, "identity"), string(i, "enrollment_id"), string(i, "kind"),
          string(i, "credential_id"), string(i, "wrapped_digest"),
          string(i, "issued_at"), string(i, "nonce"),
        );
      case "keystore-list":
        return canonicalKeystoreList(
          string(i, "identity"), string(i, "issued_at"), string(i, "nonce"),
        );
      case "keystore-remove":
        return canonicalKeystoreRemove(
          string(i, "identity"), string(i, "enrollment_id"),
          string(i, "issued_at"), string(i, "nonce"),
        );
      case "session-registration":
        return canonicalSessionRegistration(
          string(i, "identity"), string(i, "session_public_key"),
          string(i, "issued_at"), string(i, "expires_at"), string(i, "nonce"),
        );
      case "session-revocation":
        return canonicalSessionRevocation(
          string(i, "identity"), string(i, "session_id"),
          string(i, "issued_at"), string(i, "nonce"),
        );
      default:
        throw new Error(`no TypeScript derivation for vector "${vector.name}" — add one`);
    }
  }

  it("covers every vector Go emits", () => {
    expect(file.vectors.length).toBeGreaterThan(0);
    // A new vector with no case above throws, which is the point: adding a
    // canonical string in Go forces the TS side to implement it.
    for (const vector of file.vectors) expect(() => derive(vector)).not.toThrow();
  });

  for (const vector of loadVectors<CanonicalFile>("canonical").vectors) {
    it(`derives ${vector.name}`, () => {
      expect(derive(vector)).toBe(vector.canonical);
    });

    it(`signs ${vector.name} to the same bytes`, () => {
      expect(signCanonical(seed, vector.canonical)).toBe(vector.signature);
      expect(verifyCanonical(publicKey, vector.canonical, vector.signature)).toBe(true);
    });
  }
});

describe("identity documents match Go", () => {
  interface DocumentVector {
    name: string;
    document: IdentityDocument;
    canonical: string;
    signature: string;
    key_valid?: Record<string, boolean>;
  }
  const vectors = loadVectors<DocumentVector[]>("documents");

  for (const vector of vectors) {
    it(`canonicalizes and verifies ${vector.name}`, () => {
      expect(canonicalDocument(vector.document)).toBe(vector.canonical);
      expect(() => verifyDocument(vector.document)).not.toThrow();
    });
  }

  it("rejects a tampered document", () => {
    const original = vectors[0] as DocumentVector;
    const tampered = { ...original.document, relay: "evil.example.org" };
    expect(() => verifyDocument(tampered)).toThrow(/signature invalid/);
  });

  it("agrees with Go on rotation grace windows", () => {
    const rotated = vectors.find((v) => v.key_valid);
    expect(rotated).toBeDefined();
    const at = new Date("2026-01-20T00:00:00Z");
    for (const [key, expected] of Object.entries(rotated?.key_valid ?? {})) {
      expect(keyValidAt(rotated?.document as IdentityDocument, key, at)).toBe(expected);
    }
  });
});

describe("share grants and groups match Go", () => {
  interface GrantVector { name: string; grant: ShareGrant; canonical: string }
  interface GroupVector { name: string; group: ShareGroup; canonical: string }
  interface PathVector { input: string; path?: string; error: boolean }

  const publicKey = loadVectors<CanonicalFile>("canonical").public_key_base64url;

  for (const vector of loadVectors<GrantVector[]>("grants")) {
    it(`canonicalizes grant ${vector.name}`, () => {
      expect(canonicalShareGrant(vector.grant)).toBe(vector.canonical);
      expect(verifyGrantSignature(vector.grant, publicKey)).toBe(true);
    });
  }

  for (const vector of loadVectors<GroupVector[]>("groups")) {
    it(`canonicalizes group ${vector.name}`, () => {
      expect(canonicalShareGroup(vector.group)).toBe(vector.canonical);
      expect(verifyGroupSignature(vector.group, publicKey)).toBe(true);
    });
  }

  it("normalizes grant paths the same way", () => {
    for (const vector of loadVectors<PathVector[]>("grant-paths")) {
      if (vector.error) {
        expect(() => normalizeGrantPath(vector.input), vector.input).toThrow();
      } else {
        expect(normalizeGrantPath(vector.input), vector.input).toBe(vector.path);
      }
    }
  });
});

describe("proof-of-work matches Go", () => {
  interface PowVector { token: string; bits: number; solution: string; valid: boolean }
  interface ClampVector { input: number; output: number }

  it("agrees on whether a solution satisfies a difficulty", () => {
    for (const vector of loadVectors<PowVector[]>("pow")) {
      expect(checkPow(vector.token, vector.solution, vector.bits), vector.token).toBe(vector.valid);
    }
  });

  it("clamps difficulty into the same window", () => {
    for (const vector of loadVectors<ClampVector[]>("pow-clamp")) {
      expect(clampPowBits(vector.input), String(vector.input)).toBe(vector.output);
    }
  });
});

describe("name policy matches Go", () => {
  interface NameVector {
    identity: string;
    valid: boolean;
    hosted_valid: boolean;
    dir_name?: string;
  }

  for (const vector of loadVectors<NameVector[]>("names")) {
    it(`agrees on "${vector.identity}"`, () => {
      expect(isValidIdentityName(vector.identity)).toBe(vector.valid);
      let hostedValid = true;
      try {
        validateHostedHandle(vector.identity);
      } catch {
        hostedValid = false;
      }
      expect(hostedValid).toBe(vector.hosted_valid);
      if (vector.dir_name) {
        expect(sanitizeIdentityDirName(vector.identity)).toBe(vector.dir_name);
      }
    });
  }
});

describe("key fingerprints match Go", () => {
  interface FingerprintVector {
    name: string;
    key: string;
    kind: "signing" | "encryption";
    fingerprint: string;
    valid: boolean;
  }

  for (const vector of loadVectors<FingerprintVector[]>("fingerprints")) {
    it(`derives the same short auth string for "${vector.name}"`, () => {
      const derive = () =>
        vector.kind === "encryption"
          ? encryptionKeyFingerprint(vector.key)
          : keyFingerprint(vector.key);
      if (!vector.valid) {
        expect(derive).toThrow();
        // The display helper still has to hand the raw value back.
        expect(fingerprintOrKey(vector.key)).toBe(vector.key);
        return;
      }
      expect(derive()).toBe(vector.fingerprint);
      if (vector.kind === "signing") {
        expect(fingerprintOrKey(vector.key)).toBe(vector.fingerprint);
      }
    });
  }
});

describe("poweur-sys documents match Go", () => {
  interface SysDocVector { name: string; raw: unknown; valid: boolean }

  it("accepts and rejects the same contacts.json documents", () => {
    for (const vector of loadVectors<SysDocVector[]>("contacts")) {
      let valid = true;
      try {
        validateContactsFile(vector.raw as ContactsFile);
      } catch {
        valid = false;
      }
      expect(valid, vector.name).toBe(vector.valid);
    }
  });

  it("accepts and rejects the same profile.json documents", () => {
    for (const vector of loadVectors<SysDocVector[]>("profiles")) {
      let valid = true;
      try {
        validateProfile(vector.raw as Profile);
      } catch {
        valid = false;
      }
      expect(valid, vector.name).toBe(vector.valid);
    }
  });

  it("accepts and rejects the same inbox-policy.json documents", () => {
    for (const vector of loadVectors<SysDocVector[]>("inbox-policies")) {
      let valid = true;
      try {
        validateInboxPolicy(vector.raw as InboxPolicy);
      } catch {
        valid = false;
      }
      expect(valid, vector.name).toBe(vector.valid);
    }
  });
});

describe("message encryption interoperates with Go", () => {
  interface EncryptionVector {
    name: string;
    recipient_private_key: string;
    recipient_public_key: string;
    plaintext: string;
    alg: string;
    ciphertext: string;
    ephemeral_public_key: string;
    nonce: string;
  }

  for (const vector of loadVectors<EncryptionVector[]>("encryption")) {
    it(`decrypts the Go-sealed "${vector.name}" payload`, () => {
      const plaintext = decryptMessage(
        fromBase64(vector.recipient_private_key),
        vector.ciphertext,
        {
          alg: vector.alg,
          ephemeral_public_key: vector.ephemeral_public_key,
          nonce: vector.nonce,
        },
      );
      expect(toBase64url(new TextEncoder().encode(plaintext))).toBe(vector.plaintext);
      expect(plaintext).toBe(fromUtf8(fromBase64(vector.plaintext)));
    });

    it(`rejects a tampered "${vector.name}" payload`, () => {
      // Flip a bit in the decoded ciphertext, not a base64 character: an
      // unpadded base64 string carries unused bits in its last character, so
      // changing that character does not reliably change the bytes.
      const bytes = fromBase64(vector.ciphertext);
      bytes[0] = (bytes[0] as number) ^ 0xff;
      expect(() =>
        decryptMessage(fromBase64(vector.recipient_private_key), toBase64url(bytes), {
          alg: vector.alg,
          ephemeral_public_key: vector.ephemeral_public_key,
          nonce: vector.nonce,
        }),
      ).toThrow(/authentication failed|malformed/);
    });
  }
});

describe("message history (Go vectors)", () => {
  interface HistoryPathVector {
    name: string;
    timestamp: string;
    id: string;
    path: string;
  }

  interface ReadStateVector {
    name: string;
    raw: unknown;
    valid: boolean;
    unread?: Record<string, number>;
    marks?: Record<string, { timestamp: string; id?: string }>;
  }

  // The archive path is the one thing two implementations absolutely must
  // agree on: disagree, and the same message is filed twice — once by the web
  // app and once by the CLI — and neither knows the other's copy exists.
  for (const vector of loadVectors<HistoryPathVector[]>("history-paths")) {
    it(`files "${vector.name}" at the same path as Go`, () => {
      expect(historyPath(vector.timestamp, vector.id)).toBe(vector.path);
    });
  }

  // The owner's own tree is the target, so a sender-chosen id that escapes
  // its segment is a write into someone else's config, not a cosmetic bug.
  it("never lets a sender's id escape its path segment", () => {
    for (const id of ["../../poweur-sys/relay/contacts", "a/b", "..", "", ".poweur-web-public"]) {
      const name = historyFileName("2026-01-15T09:30:00Z", id);
      expect(name).not.toMatch(/[/\\]/);
      expect(name).not.toContain("..");
      expect(name.startsWith(".")).toBe(false);
    }
  });

  const owner = "alice.example.org";
  const records: HistoryRecord[] = [
    { id: "m1", sender: "bob.example.org", recipient: owner, timestamp: "2026-01-15T09:00:00Z", queue: "inbox", body: "one" },
    { id: "m2", sender: "bob.example.org", recipient: owner, timestamp: "2026-01-15T09:30:00Z", queue: "inbox", body: "two" },
    { id: "m3", sender: owner, recipient: "bob.example.org", timestamp: "2026-01-15T09:45:00Z", queue: "sent", body: "reply" },
    { id: "m4", recipient: owner, timestamp: "2026-01-15T10:00:00Z", queue: "anonymous", body: "tip" },
  ];

  for (const vector of loadVectors<ReadStateVector[]>("history-read-state")) {
    it(`agrees with Go on read state "${vector.name}"`, () => {
      const raw = JSON.stringify(vector.raw);
      if (!vector.valid) {
        expect(() => parseReadState(raw)).toThrow();
        return;
      }
      const state = parseReadState(raw);
      expect(unreadCounts(state, owner, records)).toEqual(vector.unread ?? {});
      const marked = markRead(state, "BOB.example.org", "2026-01-15T09:30:00Z", "m2");
      expect(marked.conversations).toEqual(vector.marks ?? {});
    });
  }
});
