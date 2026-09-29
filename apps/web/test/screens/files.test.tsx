import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Files } from "../../src/screens/files/Files";
import { useData } from "../../src/state/data";
import { useSession } from "../../src/state/session";
import { resetStores } from "../helpers/stores";

const mocks = vi.hoisted(() => {
  const root = { manifest: { node: "root", kind: "folder" }, name: "", folder: "", nodeKey: new Uint8Array(32) } as any;
  const note = { manifest: { node: "note", kind: "file", mode: "replace" }, name: "note.txt", folder: "root", nodeKey: new Uint8Array(32), contentKey: new Uint8Array(32) } as any;
  const files = {
    client: { drive: "alice.poweur.net" },
    root: vi.fn(async () => root),
    list: vi.fn(async () => [note]),
    open: vi.fn(async () => note),
    create: vi.fn(async () => note),
    replace: vi.fn(async () => {}),
    remove: vi.fn(async () => {}),
  };
  return {
    root, note, files,
    openBrowserDrive: vi.fn(async () => ({ files, drive: { shares: vi.fn(async () => ({ shares: [] })) } })),
    readFileBytes: vi.fn(async () => new TextEncoder().encode("hello")),
    loadMounts: vi.fn(async () => []),
    acceptBrowserOffer: vi.fn(),
    shareBrowserFile: vi.fn(),
    sharesForFile: vi.fn(async () => []),
    revokeBrowserShare: vi.fn(),
  };
});

vi.mock("../../src/lib/drive", () => ({ openBrowserDrive: mocks.openBrowserDrive, readFileBytes: mocks.readFileBytes }));
vi.mock("../../src/actions/files", () => ({
  loadMounts: mocks.loadMounts,
  acceptBrowserOffer: mocks.acceptBrowserOffer,
  shareBrowserFile: mocks.shareBrowserFile,
  sharesForFile: mocks.sharesForFile,
  revokeBrowserShare: mocks.revokeBrowserShare,
  shareOffers: (messages: any[]) => messages.filter((message) => message.type === "sys.share.offer").map((message) => JSON.parse(message.plaintext)),
}));

beforeEach(() => {
  resetStores();
  vi.clearAllMocks();
  mocks.files.list.mockResolvedValue([mocks.note]);
  mocks.loadMounts.mockResolvedValue([]);
  useSession.setState({ identity: "alice.poweur.net", unlocked: true });
});

describe("Files destination (E20-T10)", () => {
  it("lists encrypted drive entries and exposes upload and folder actions", async () => {
    const { container } = render(<Files />);
    await screen.findByText("note.txt");
    expect(container.querySelector("#btn-upload-file")).toBeTruthy();
    expect(container.querySelector("#btn-new-folder")).toBeTruthy();
    expect(mocks.files.root).toHaveBeenCalled();
    expect(mocks.files.list).toHaveBeenCalledWith(mocks.root);
  });

  it("shows pending offers and accepts one into Shared with me", async () => {
    const offer = { format: 2, share: { id: "a".repeat(32), issuer: "bob.poweur.net", role: "read" }, name: "Plans", kind: "folder" };
    useData.setState({ messages: [{ type: "sys.share.offer", plaintext: JSON.stringify(offer), recipient: "alice.poweur.net" }] });
    mocks.acceptBrowserOffer.mockResolvedValue({ drive: "bob.poweur.net", relay: "bob.poweur.net", node: "b".repeat(32), share_id: offer.share.id, name: "Plans", kind: "folder", role: "read", mounted: "2026-09-29T00:00:00Z" });

    render(<Files />);
    fireEvent.click(await screen.findByRole("tab", { name: "Shared with me" }));
    expect(await screen.findByText("Plans")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() => expect(mocks.acceptBrowserOffer).toHaveBeenCalledWith("alice.poweur.net", offer));
  });
});
