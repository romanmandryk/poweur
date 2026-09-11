import { describe, expect, it } from "vitest";

import { canonicalShareGroup } from "../src/canonical.js";
import { decryptMessage } from "../src/crypto/index.js";
import { generateIdentityKeys, signerFor } from "../src/crypto/keys.js";
import { newDocument, signDocumentWithKey } from "../src/document.js";
import { GroupMessaging, groupThreadId, validateGroupThreadId } from "../src/groups.js";
import { RelayClient } from "../src/http.js";
import type { IdentityDocument, Message, ShareGroup } from "../src/types.js";

async function fixture() {
  const aliceKeys = generateIdentityKeys("alice.example.org");
  const bobKeys = generateIdentityKeys("bob.example.org");
  const groupKeys = generateIdentityKeys("crew.example.org");
  const alice = signerFor(aliceKeys).signer;
  const bob = signerFor(bobKeys);
  const groupSigner = signerFor(groupKeys).signer;

  const identityDocument = (identity: string, keys: typeof aliceKeys, relay: string): IdentityDocument => {
    const pair = signerFor(keys);
    return signDocumentWithKey(newDocument({
      identity,
      publicKey: pair.signer.publicKey,
      encryptionPublicKey: pair.decryptor!.encryptionPublicKey,
      relay,
      updatedAt: "2026-09-10T20:00:00Z",
    }), keys.signingPrivateKey);
  };
  const docs = new Map([
    ["crew.example.org", identityDocument("crew.example.org", groupKeys, "groups.relay.test")],
    ["alice.example.org", identityDocument("alice.example.org", aliceKeys, "alice.relay.test")],
    ["bob.example.org", identityDocument("bob.example.org", bobKeys, "bob.relay.test")],
  ]);
  const unsigned: Omit<ShareGroup, "signature"> = {
    group: "crew.example.org",
    owner: "crew.example.org",
    members: ["alice.example.org", "bob.example.org"],
    admins: ["alice.example.org"],
    epoch: 3,
    updated_at: "2026-09-10T20:01:00Z",
  };
  const roster: ShareGroup = {
    ...unsigned,
    signature: await groupSigner.sign(canonicalShareGroup(unsigned), "base64url"),
  };
  let posted: { envelopes: Message[] } | null = null;
  const fetchImpl = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    for (const [identity, doc] of docs) {
      if (url === `https://${identity}/.well-known/poweur/id.json`) {
        return new Response(JSON.stringify(doc), { status: 200 });
      }
    }
    if (url.includes("/auth/challenge?")) return new Response(JSON.stringify({ challenge: "one-use" }), { status: 200 });
    if (url.endsWith("/groups/crew.example.org") && init?.method === "GET") {
      return new Response(JSON.stringify(roster), { status: 200 });
    }
    if (url.endsWith("/groups/crew.example.org/messages") && init?.method === "POST") {
      posted = JSON.parse(String(init.body)) as { envelopes: Message[] };
      return new Response(JSON.stringify({
        group: roster.group,
        epoch: roster.epoch,
        delivered: posted.envelopes.map((message) => ({ recipient: message.recipient, id: message.id, status: "accepted" })),
        failed: [],
      }), { status: 202 });
    }
    return new Response("not found", { status: 404 });
  }) as typeof globalThis.fetch;
  const groups = new GroupMessaging({
    client: new RelayClient("https://alice.relay.test", { fetch: fetchImpl }),
    resolve: { fetch: fetchImpl, skipDns: true },
  });
  return { alice, bob, bobKeys, groups, getPosted: () => posted };
}

describe("group messaging", () => {
  it("encrypts one signed envelope per other member and posts one batch", async () => {
    const f = await fixture();
    const result = await f.groups.send(f.alice, "crew.example.org", "hello crew", { thread: "plans" });
    expect(result.response.delivered).toHaveLength(1);
    const posted = f.getPosted();
    expect(posted).not.toBeNull();
    const message = posted!.envelopes[0]!;
    expect(message.recipient).toBe("bob.example.org");
    expect(message.thread_id).toBe("crew.example.org:plans");
    expect(message.metadata).toEqual({ group: "crew.example.org", epoch: "3" });
    expect(decryptMessage(await f.bob.decryptor!.privateKeyBytes(), message.payload, message.encryption!)).toBe("hello crew");
  });

  it("does not allow callers to forge reserved group metadata", async () => {
    const f = await fixture();
    await expect(f.groups.send(f.alice, "crew.example.org", "no", {
      metadata: { group: "other.example.org" },
    })).rejects.toThrow(/reserved/);
  });

  it("builds only group-owned thread ids", () => {
    expect(groupThreadId("crew.example.org", "Plans")).toBe("crew.example.org:plans");
    expect(validateGroupThreadId("crew.example.org", "crew.example.org:plans")).toBeNull();
    expect(validateGroupThreadId("crew.example.org", "other-thread")).toMatch(/must be/);
  });
});
