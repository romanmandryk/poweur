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
    linkBrowserFile: vi.fn(),
    fileRequestBrowserLink: vi.fn(),
    ensureBrowserFiles: vi.fn(),
    loadBrowserFolder: vi.fn(),
    refreshBrowserFiles: vi.fn(),
  };
});

vi.mock("../../src/lib/drive", () => ({ openBrowserDrive: mocks.openBrowserDrive, readFileBytes: mocks.readFileBytes }));
vi.mock("../../src/actions/files", () => ({
  loadMounts: mocks.loadMounts,
  acceptBrowserOffer: mocks.acceptBrowserOffer,
  autoAcceptContactOffers: vi.fn(async () => 0),
  shareBrowserFile: mocks.shareBrowserFile,
  sharesForFile: mocks.sharesForFile,
  revokeBrowserShare: mocks.revokeBrowserShare,
  linkBrowserFile: mocks.linkBrowserFile,
  fileRequestBrowserLink: mocks.fileRequestBrowserLink,
  ensureBrowserFiles: mocks.ensureBrowserFiles,
  loadBrowserFolder: mocks.loadBrowserFolder,
  refreshBrowserFiles: mocks.refreshBrowserFiles,
  cachedBrowserFolder: (files: any, folder: any) => useData.getState().files.folders[`${files.client.drive}:${folder.manifest.node}`]?.entries,
  shareOffers: (messages: any[]) => messages.filter((message) => message.type === "sys.share.offer").map((message) => JSON.parse(message.plaintext)),
}));

beforeEach(() => {
  resetStores();
  vi.clearAllMocks();
  mocks.files.list.mockResolvedValue([mocks.note]);
  mocks.loadMounts.mockResolvedValue([]);
  useSession.setState({ identity: "alice.poweur.net", unlocked: true });
  mocks.ensureBrowserFiles.mockImplementation(async () => {
    useData.setState((state) => ({ files: {
      ...state.files,
      own: { files: mocks.files as any, root: mocks.root },
      folders: { [`alice.poweur.net:${mocks.root.manifest.node}`]: { folder: mocks.root, entries: [mocks.note] } },
      loaded: true,
    } }));
  });
  mocks.loadBrowserFolder.mockImplementation(async (_identity: string, files: any, folder: any) => {
    const entries = await files.list(folder);
    useData.setState((state) => ({ files: { ...state.files, folders: { ...state.files.folders, [`${files.client.drive}:${folder.manifest.node}`]: { folder, entries } } } }));
    return entries;
  });
  mocks.refreshBrowserFiles.mockResolvedValue(undefined);
});

describe("Files destination (E20-T10)", () => {
  it("lists encrypted drive entries and exposes upload and folder actions", async () => {
    const { container } = render(<Files />);
    await screen.findByText("note.txt");
    expect(container.querySelector("#btn-upload-file")).toBeTruthy();
    expect(container.querySelector("#btn-new-folder")).toBeTruthy();
    expect(container.querySelector(".pull-to-refresh .dest-title")?.textContent).toBe("Files");
    expect(container.querySelector(".pull-to-refresh [role=tablist]")).toBeTruthy();
    expect(mocks.ensureBrowserFiles).toHaveBeenCalledWith("alice.poweur.net");
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
