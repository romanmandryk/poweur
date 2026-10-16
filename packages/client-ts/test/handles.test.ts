import { describe, expect, it, vi } from "vitest";
import { HANDLES_PATH, lookupFediverseAccount, normalizeNostrPubkey, writeHandles } from "../src/handles.js";

const HEX = "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d";
// The NIP-19 example key.
const NPUB = "npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6";

describe("Nostr keys", () => {
  it("accepts hex and npub, refuses anything else", () => {
    expect(normalizeNostrPubkey(HEX)).toBe(HEX);
    expect(normalizeNostrPubkey(HEX.toUpperCase())).toBe(HEX);
    expect(normalizeNostrPubkey(NPUB)).toBe(HEX);
    expect(normalizeNostrPubkey(` nostr:${NPUB} `)).toBe(HEX);
    expect(normalizeNostrPubkey(NPUB.slice(0, -1) + "q")).toBeNull(); // bad checksum
    expect(normalizeNostrPubkey("nsec1" + NPUB.slice(5))).toBeNull();
    expect(normalizeNostrPubkey("hello")).toBeNull();
  });
});

describe("writeHandles", () => {
  const fakeFiles = () => ({ writeJson: vi.fn(async () => null), remove: vi.fn(async () => true) });

  it("writes only what is set", async () => {
    const files = fakeFiles();
    await writeHandles(files as never, { version: 1, atproto_did: " did:plc:z72i7hdynmk6r22z27h6tvur ", nostr_pubkey: "", nostr_relays: ["wss://x"] });
    expect(files.writeJson).toHaveBeenCalledWith(HANDLES_PATH, { version: 1, atproto_did: "did:plc:z72i7hdynmk6r22z27h6tvur" });
  });

  it("removes the document when nothing is linked", async () => {
    const files = fakeFiles();
    await writeHandles(files as never, { version: 1 });
    expect(files.remove).toHaveBeenCalledWith(HANDLES_PATH);
    expect(files.writeJson).not.toHaveBeenCalled();
  });

  it("refuses an invalid DID before writing", async () => {
    const files = fakeFiles();
    await expect(writeHandles(files as never, { version: 1, atproto_did: "alice.bsky.social" })).rejects.toThrow(/did:plc/);
    expect(files.writeJson).not.toHaveBeenCalled();
  });
});

describe("lookupFediverseAccount", () => {
  const jrd = (body: unknown, status = 200) =>
    vi.fn(async (_url: string) => new Response(JSON.stringify(body), { status })) as unknown as typeof globalThis.fetch;

  it("asks the account's own server and keeps its canonical acct and actor", async () => {
    const fetch = jrd({ subject: "acct:Alice@mastodon.social", links: [{ rel: "self", type: "application/activity+json", href: "https://mastodon.social/users/Alice" }] });
    await expect(lookupFediverseAccount(" @Alice@Mastodon.Social ", fetch)).resolves.toEqual({ acct: "Alice@mastodon.social", actor: "https://mastodon.social/users/Alice" });
    expect((fetch as any).mock.calls[0][0]).toBe("https://mastodon.social/.well-known/webfinger?resource=acct%3AAlice%40mastodon.social");
  });

  it("refuses bad input, unknown accounts and non-https actors", async () => {
    await expect(lookupFediverseAccount("alice", jrd({}))).rejects.toThrow(/@alice@mastodon.social/);
    await expect(lookupFediverseAccount("@a@x.social", jrd({}, 404))).rejects.toThrow(/does not know/);
    await expect(lookupFediverseAccount("@a@x.social", jrd({ links: [{ rel: "self", type: "application/activity+json", href: "http://x.social/u/a" }] }))).rejects.toThrow(/did not return/);
  });
});
