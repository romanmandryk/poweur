import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, waitFor } from "@testing-library/react";

const holder = vi.hoisted(() => ({ client: null as any, changes: [] as any[] }));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => holder.client,
}));
vi.mock("../../src/lib/profiles.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  resolveProfile: vi.fn((identity: string) => Promise.resolve({ identity, displayName: null, links: [], capabilities: { features: {} } })),
}));
vi.mock("@poweur/client", async (importOriginal) => {
  const actual = await importOriginal<any>();
  return {
    ...actual,
    streamForever: vi.fn(() => new Promise(() => {})),
    SyncClient: class {
      async changes() {
        const changes = holder.changes.splice(0);
        return { changes, cursor: "c1", fullResync: false };
      }
      async uploadChunked() {}
    },
  };
});

import { resetContactsForTests } from "../../src/actions/contacts";
import { resetFilesForTests } from "../../src/actions/files";
import { resetMessagingForTests } from "../../src/actions/messages";
import { App } from "../../src/shell/App";
import { useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { useSession } from "../../src/state/session";
import { fakeClient } from "../helpers/fake-client";
import { resetStores } from "../helpers/stores";

const ME = "alice.poweur.net";
const BOB = "bob.poweur.net";
const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);
const $$ = (selector: string) => document.querySelectorAll(selector);

const dir = (path: string) => ({ name: path.split("/").pop(), path, dir: true, size: 0 });
const file = (path: string, size = 12) => ({ name: path.split("/").pop(), path, dir: false, size });

/** A relay tree the fake DAV client serves, and the writes it records. */
function relayTree() {
  const tree: Record<string, any[]> = {
    "": [dir("private"), dir("shared"), dir("public")],
    shared: [dir("shared/project-x")],
    "shared/project-x": [file("shared/project-x/notes.txt")],
  };
  const grants: any[] = [];
  const davClient = {
    identity: ME,
    token: "token",
    list: vi.fn(async (path: string) => tree[path] ?? []),
    quota: vi.fn(async () => ({ used_bytes: 1024, quota_bytes: 10 * 1024, provider: "relay-fs" })),
    write: vi.fn(async (path: string) => {
      const parent = path.split("/").slice(0, -1).join("/").replace(/^\//, "");
      tree[parent] = [...(tree[parent] ?? []), file(path.replace(/^\//, ""))];
    }),
    readBytes: vi.fn(async () => new Uint8Array([104, 105])),
    mkdir: vi.fn(async () => {}),
    move: vi.fn(async () => {}),
    remove: vi.fn(async () => {}),
  };
  const shares = {
    list: vi.fn(async () => [...grants]),
    listMounts: vi.fn(async () => []),
    add: vi.fn(async (_signer: unknown, path: string, options: any) => {
      grants.push({
        share_id: `s${grants.length + 1}`,
        path,
        audience: options.with.map((id: string) => ({ id })),
        permissions: options.permissions === "rw" ? ["read", "write"] : ["read"],
        ...(options.expiresAt ? { expires_at: options.expiresAt } : {}),
      });
    }),
    addFileRequest: vi.fn(async (_signer: unknown, path: string, options: any) => {
      const grant = {
        share_id: `s${grants.length + 1}`,
        path,
        audience: [{ link: "aaaaaaaaaaaaaaaaaaaaaaaaaa" }],
        permissions: ["create"],
        link: { file_request: { max_uploads: options.maxUploads, max_object_bytes: options.maxObjectBytes } },
      };
      grants.push(grant);
      return { grant, token: "aaaaaaaaaaaaaaaaaaaaaaaaaa" };
    }),
    revoke: vi.fn(async (id: string) => {
      grants.splice(grants.findIndex((grant) => grant.share_id === id), 1);
      return true;
    }),
  };
  holder.client.dav = vi.fn(async () => davClient);
  holder.client.shares = vi.fn(async () => shares);
  holder.client.offerShare = vi.fn(async (path: string, audience: string[], options: any) => {
    await shares.add(holder.client.signer, path, { with: audience, ...options });
    return { grant: grants.at(-1), deliveries: audience.map((recipient) => ({ recipient, delivered: true })) };
  });
  holder.client.revokeShareAndNotify = vi.fn(async (shareId: string) => ({
    revoked: await shares.revoke(shareId), notifications: [],
  }));
  return { tree, davClient, shares };
}

beforeEach(() => {
  resetStores();
  resetMessagingForTests();
  resetContactsForTests();
  resetFilesForTests();
  holder.client = fakeClient();
  holder.changes = [];
  useSession.setState({ identity: ME, unlocked: true });
  useRoute.setState({ page: "files" });
});

const openFolder = async (path: string) => {
  await waitFor(() => expect($(`[data-open-dir="${path}"]`)).toBeTruthy());
  fireEvent.click($(`[data-open-dir="${path}"]`)!);
  await waitFor(() => expect(useData.getState().files.path).toBe(path));
};

describe("Files destination (E21-T9)", () => {
  it("lists the roots with their badges and the quota", async () => {
    relayTree();
    render(<App />);
    await waitFor(() => expect($$(".file-row")).toHaveLength(3));
    expect($('[data-open-dir="shared"]')!.textContent).toContain("grants");
    expect(document.body.textContent).toContain("1.0 KB of 10 KB used");
    // At the root: the shares list, but no upload or new folder.
    expect($("#btn-shares")).toBeTruthy();
    expect($("#ff-upload")).toBeNull();
  });

  it("opens folders from rows and from the keyboard, and walks back by breadcrumb", async () => {
    relayTree();
    render(<App />);
    await openFolder("shared");
    fireEvent.keyDown($('[data-open-dir="shared/project-x"]')!, { key: "Enter" });
    await waitFor(() => expect($(".breadcrumbs")!.textContent).toContain("project-x"));
    expect($(".conv-name")!.textContent).toContain("notes.txt");

    fireEvent.click($('[data-nav-path=""]')!);
    await waitFor(() => expect(useData.getState().files.path).toBe(""));
  });

  it("uploads into the open folder and lists the result", async () => {
    const { davClient } = relayTree();
    render(<App />);
    await openFolder("shared");
    await openFolder("shared/project-x");

    const upload = $<HTMLInputElement>("#ff-upload")!;
    const picked = new File(["written from the browser"], "hello.txt", { type: "text/plain" });
    fireEvent.change(upload, { target: { files: [picked] } });

    await waitFor(() => expect(davClient.write).toHaveBeenCalledWith("shared/project-x/hello.txt", picked));
    await waitFor(() => expect($(".conv-list")!.textContent).toContain("hello.txt"));
    expect($(".toast.success")!.textContent).toContain("Uploaded 1 file");
  });

  it("creates, renames and deletes through dialogs, not prompt()", async () => {
    const { davClient } = relayTree();
    render(<App />);
    await openFolder("shared");

    fireEvent.click($("#btn-new-folder")!);
    await waitFor(() => expect($("#dialog-text")).toBeTruthy());
    fireEvent.change($("#dialog-text")!, { target: { value: "  drafts " } });
    $<HTMLInputElement>("#dialog-text")!.value = "  drafts ";
    fireEvent.click($("#dialog-ok")!);
    await waitFor(() => expect(davClient.mkdir).toHaveBeenCalledWith("shared/drafts"));

    fireEvent.click($('[data-rename="shared/project-x"]')!);
    await waitFor(() => expect($<HTMLInputElement>("#dialog-text")?.value).toBe("project-x"));
    $<HTMLInputElement>("#dialog-text")!.value = "project-y";
    fireEvent.click($("#dialog-ok")!);
    await waitFor(() => expect(davClient.move).toHaveBeenCalledWith("shared/project-x", "shared/project-y"));

    fireEvent.click($('[data-delete="shared/project-x"]')!);
    await waitFor(() => expect($("#panel-confirm-delete")).toBeTruthy());
    fireEvent.click($("#dialog-cancel")!);
    await waitFor(() => expect($("#panel-root")).toBeNull());
    expect(davClient.remove).not.toHaveBeenCalled();

    fireEvent.click($('[data-delete="shared/project-x"]')!);
    await waitFor(() => expect($("#panel-confirm-delete")).toBeTruthy());
    fireEvent.click($("#panel-confirm-delete")!);
    await waitFor(() => expect(davClient.remove).toHaveBeenCalledWith("shared/project-x"));
  });

  it("downloads a file", async () => {
    const { davClient } = relayTree();
    URL.createObjectURL = vi.fn(() => "blob:fake");
    URL.revokeObjectURL = vi.fn();
    render(<App />);
    await openFolder("shared");
    await openFolder("shared/project-x");
    fireEvent.click($('[data-download="shared/project-x/notes.txt"]')!);
    await waitFor(() => expect(davClient.readBytes).toHaveBeenCalledWith("shared/project-x/notes.txt"));
  });

  it("shares a folder read-write, shows it, and revokes it", async () => {
    const { shares } = relayTree();
    render(<App />);
    await openFolder("shared");
    // Only paths under a shareable root get a share button, never the root itself.
    expect($('[data-share="shared/project-x"]')).toBeTruthy();

    fireEvent.click($('[data-share="shared/project-x"]')!);
    await waitFor(() => expect($("#btn-share-go")).toBeTruthy());
    expect($<HTMLButtonElement>("#btn-share-go")!.disabled).toBe(true);

    await act(async () => {
      const picker = $<HTMLInputElement>(".audience-picker .idin input")!;
      picker.value = BOB;
      fireEvent.keyDown(picker, { key: "Enter" });
    });
    await waitFor(() => expect($(".audience-chips")!.textContent).toContain("bob"));
    fireEvent.click($('#share-perms [data-perm="rw"]')!);
    expect($('#share-perms [data-perm="rw"]')!.classList.contains("selected")).toBe(true);
    fireEvent.click($("#btn-share-go")!);

    await waitFor(() =>
      expect(shares.add).toHaveBeenCalledWith(expect.anything(), "shared/project-x", { with: [BOB], permissions: "rw" }),
    );
    await waitFor(() => expect($('[data-open-dir="shared/project-x"] .chip-accent')?.textContent).toBe("Shared"));

    fireEvent.click($('[data-nav-path=""]')!);
    await waitFor(() => expect($("#btn-shares")).toBeTruthy());
    fireEvent.click($("#btn-shares")!);
    await waitFor(() => expect($(".share-path")?.textContent).toBe("/shared/project-x"));
    expect($(".share-row")!.textContent).toContain("read + write");
    expect($(".share-row")!.textContent).toContain(BOB);

    fireEvent.click($("[data-revoke]")!);
    await waitFor(() => expect($("#shares-list")!.textContent).toContain("not shared anything yet"));
    expect(shares.revoke).toHaveBeenCalledWith("s1");
  });

  it("creates an upload-only file request and shows its URL", async () => {
    const { shares } = relayTree();
    render(<App />);
    await openFolder("shared");
    fireEvent.click($('[data-share="shared/project-x"]')!);
    await waitFor(() => expect($("#btn-share-go")).toBeTruthy());
    fireEvent.click(Array.from($$("button")).find((button) => button.textContent === "Request files")!);
    const max = $<HTMLInputElement>("#request-max-uploads")!;
    fireEvent.change(max, { target: { value: "2" } });
    fireEvent.click($("#btn-share-go")!);
    await waitFor(() => expect(shares.addFileRequest).toHaveBeenCalledWith(
      expect.anything(),
      "shared/project-x",
      expect.objectContaining({ maxUploads: 2 }),
    ));
    await waitFor(() => expect($<HTMLInputElement>("#file-request-url")?.value).toBe(
      "https://alice.poweur.net/s/aaaaaaaaaaaaaaaaaaaaaaaaaa",
    ));
  });

  it("opens someone else's tree from the owner picker, and leaves it", async () => {
    relayTree();
    holder.client.contactsApi.load.mockResolvedValue({ contacts: [{ identity: BOB, state: "accepted", petname: "Bobby" }] });
    render(<App />);
    await waitFor(() => expect($("#btn-files-shared")).toBeTruthy());
    fireEvent.click($("#btn-files-shared")!);
    await waitFor(() => expect($(`[data-open-owner="${BOB}"]`)).toBeTruthy());
    expect($(".owner-picker .idin input")).toBeTruthy();

    fireEvent.click($(`[data-open-owner="${BOB}"]`)!);
    await waitFor(() => expect($(".visitor-banner")?.textContent).toContain(BOB));
    expect(holder.client.dav).toHaveBeenLastCalledWith({ audience: BOB, scope: "dav:full", force: true });
    // A visitor gets no share, rename or delete, and no quota.
    await waitFor(() => expect($$(".file-row").length).toBeGreaterThan(0));
    expect($("[data-share]")).toBeNull();
    expect($("[data-delete]")).toBeNull();

    fireEvent.click($("#btn-leave-owner")!);
    expect(useData.getState().files.owner).toBeNull();
    expect($(".owner-picker")).toBeTruthy();

    fireEvent.click($("#btn-files-mine")!);
    await waitFor(() => expect($(".owner-picker")).toBeNull());
  });

  it("refreshes the open folder when the changes feed touches it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const { tree } = relayTree();
      render(<App />);
      await openFolder("shared");
      await openFolder("shared/project-x");

      tree["shared/project-x"] = [...tree["shared/project-x"], file("shared/project-x/from-elsewhere.txt")];
      holder.changes.push({ path: "shared/project-x/from-elsewhere.txt", op: "put" });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000);
      });
      await waitFor(() => expect($(".conv-list")!.textContent).toContain("from-elsewhere.txt"));
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("Apps destination (E21-T10)", () => {
  it("says there are no apps yet, and offers no second identity", () => {
    useRoute.setState({ page: "launcher" });
    render(<App />);
    expect($(".dest-title")!.textContent).toBe("Apps");
    expect($(".empty-state-title")!.textContent).toBe("No apps yet");
    expect($("#ni-handle")).toBeNull();
  });
});
