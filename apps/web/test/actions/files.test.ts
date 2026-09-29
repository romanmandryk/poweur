import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  lookup: vi.fn(),
  clientFor: vi.fn(),
  openBrowserDrive: vi.fn(),
  relayUrlFor: vi.fn(() => "https://relay.example"),
}));

vi.mock("../../src/lib/client.js", () => ({ lookup: mocks.lookup, clientFor: mocks.clientFor }));
vi.mock("../../src/lib/drive", () => ({ openBrowserDrive: mocks.openBrowserDrive, readFileBytes: vi.fn() }));
vi.mock("../../src/lib/storage.js", () => ({ relayUrlFor: mocks.relayUrlFor }));

import { revokeBrowserShare, shareBrowserFile, shareOffers } from "../../src/actions/files";

const file = {
  manifest: { node: "1".repeat(32), kind: "folder" },
  name: "Plans",
  folder: "root",
  nodeKey: new Uint8Array(32),
} as any;

const validShare = {
  format: 1,
  drive: "alice.example.com",
  id: "a".repeat(32),
  node: file.manifest.node,
  member: "bob.example.com",
  role: "read",
  generation: 1,
  node_public: "A".repeat(43),
  node_key: { ephemeral_public_key: "A".repeat(43), nonce: "A".repeat(16), ciphertext: "A".repeat(64) },
  caps: {}, issuer: "alice.example.com", issued: "2026-09-29T00:00:00Z", signature: "A".repeat(86),
} as any;

beforeEach(() => vi.clearAllMocks());

describe("Files actions", () => {
  it("keeps only valid offers addressed to the active identity", () => {
    const offer = {
      format: 2,
      relay: "relay.example",
      name: "Plans",
      kind: "folder",
      offered_at: "2026-09-29T00:00:00Z",
      share: {
        format: 1,
        drive: "bob.example.com",
        id: "a".repeat(32),
        node: "b".repeat(32),
        member: "alice.example.com",
        role: "read",
        generation: 1,
        node_public: "A".repeat(43),
        node_key: { ephemeral_public_key: "A".repeat(43), nonce: "A".repeat(16), ciphertext: "A".repeat(64) },
        caps: {}, issuer: "bob.example.com", issued: "2026-09-29T00:00:00Z", signature: "A".repeat(86),
      },
    };
    const messages = [
      { type: "chat.text", recipient: "alice.example.com", plaintext: JSON.stringify(offer) },
      { type: "sys.share.offer", recipient: "carol.example.com", plaintext: JSON.stringify(offer) },
      { type: "sys.share.offer", recipient: "alice.example.com", plaintext: "{" },
      { type: "sys.share.offer", recipient: "alice.example.com", plaintext: JSON.stringify(offer) },
    ];
    expect(shareOffers(messages, "alice.example.com")).toHaveLength(1);
  });

  it("creates the signed share before sending its offer", async () => {
    const share = validShare;
    const shareWith = vi.fn(async () => share);
    const sendAndArchive = vi.fn(async () => ({}));
    mocks.lookup.mockResolvedValue({ document: { encryption_public_key: `x25519:${"A".repeat(43)}` } });
    mocks.openBrowserDrive.mockResolvedValue({ files: { shareWith }, drive: { relay: { relayUrl: "https://relay.example" } } });
    mocks.clientFor.mockReturnValue({ sendAndArchive });

    await expect(shareBrowserFile("alice.example.com", file, "BOB.example.com", "read")).resolves.toEqual({ share, notified: true });
    expect(shareWith).toHaveBeenCalledWith(file, "bob.example.com", expect.any(Uint8Array), "read");
    expect(sendAndArchive).toHaveBeenCalledWith("bob.example.com", expect.any(String), expect.objectContaining({ type: "sys.share.offer" }));
  });

  it("reports an undelivered offer without pretending the completed grant failed", async () => {
    mocks.lookup.mockResolvedValue({ document: { encryption_public_key: `x25519:${"A".repeat(43)}` } });
    mocks.openBrowserDrive.mockResolvedValue({ files: { shareWith: vi.fn(async () => validShare) }, drive: { relay: { relayUrl: "https://relay.example" } } });
    mocks.clientFor.mockReturnValue({ sendAndArchive: vi.fn(async () => { throw new Error("offline"); }) });
    await expect(shareBrowserFile("alice.example.com", file, "bob.example.com", "read")).resolves.toEqual({ share: validShare, notified: false });
  });

  it("revokes immediately even if the best-effort notice fails", async () => {
    const unshare = vi.fn(async () => ({}));
    mocks.openBrowserDrive.mockResolvedValue({ drive: { unshare } });
    mocks.clientFor.mockReturnValue({ sendAndArchive: vi.fn(async () => { throw new Error("offline"); }) });
    const share = { id: "a".repeat(32), drive: "alice.example.com", member: "bob.example.com" } as any;
    await expect(revokeBrowserShare("alice.example.com", share)).resolves.toBeUndefined();
    expect(unshare).toHaveBeenCalledWith(share.id);
  });
});
