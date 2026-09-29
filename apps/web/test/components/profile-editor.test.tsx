import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, waitFor } from "@testing-library/react";

const holder = vi.hoisted(() => ({ client: null as any, dav: null as any }));
vi.mock("../../src/lib/client.js", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  clientFor: () => holder.client,
}));

import { ProfileEditor } from "../../src/components/ProfileEditor";
import { readLocalAvatar, resetAvatarsForTests, saveLocalAvatar } from "../../src/state/avatars";
import { useData } from "../../src/state/data";
import { useSession } from "../../src/state/session";
import { Avatar } from "../../src/ui/Avatar";
import { resetStores } from "../helpers/stores";

const ME = "alice.poweur.net";
const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector<T>(selector);
const photo = () => new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 1, 2, 3])], "me.png", { type: "image/png" });

beforeEach(() => {
  resetStores();
  localStorage.clear();
  resetAvatarsForTests();
  holder.dav = {
    write: vi.fn(async () => '"etag"'),
    remove: vi.fn(async () => true),
    get: vi.fn(async () => null),
  };
  holder.client = {
    system: vi.fn(() => holder.dav),
    setProfile: vi.fn(async (doc: any) => doc),
  };
  useSession.setState({ identity: ME, unlocked: true });
});

describe("ProfileEditor photo", () => {
  it("previews a picked photo in the avatar circle before anything is uploaded", async () => {
    render(<ProfileEditor profile={null} />);
    expect($(".pe-avatar-preview img")).toBeNull();

    fireEvent.change($("#pe-avatar")!, { target: { files: [photo()] } });
    await waitFor(() => expect($(".pe-avatar-preview img")).toBeTruthy());
    expect($(".pe-avatar-preview")!.className).toContain("rounded-full");
    expect(holder.dav.write).not.toHaveBeenCalled();
    expect($("#pe-avatar-remove")).toBeTruthy();
  });

  it("uploads into .poweur/public, and every circle of ours shows it", async () => {
    render(
      <>
        <ProfileEditor profile={null} />
        <div id="elsewhere">
          <Avatar identity={ME} />
        </div>
      </>,
    );
    fireEvent.change($("#pe-avatar")!, { target: { files: [photo()] } });
    await waitFor(() => expect($(".pe-avatar-preview img")).toBeTruthy());
    fireEvent.click($("#pe-save")!);
    await waitFor(() => expect(holder.client.setProfile).toHaveBeenCalled());

    const avatar = holder.client.setProfile.mock.calls[0][0].avatar;
    expect(avatar).toMatch(/^avatar-[0-9a-z]+\.png$/);
    // Served publicly from .poweur/public at /.well-known/poweur/<name>.
    expect(holder.dav.write).toHaveBeenCalledWith(`.poweur/public/${avatar}`, expect.anything());

    await waitFor(() => expect($("#elsewhere img")?.getAttribute("src")).toMatch(/^data:image\//));
    expect(readLocalAvatar(ME)?.path).toBe(avatar);
  });

  it("removing the photo drops it from the profile, this device and .poweur/public", async () => {
    const old = "avatar-0ld.jpg";
    saveLocalAvatar(ME, old, "data:image/jpeg;base64,AAAA");
    useData.setState({ profile: { doc: { version: 1, avatar: old }, explicit: true, loaded: true, loading: false } });
    render(<ProfileEditor profile={{ avatar: old }} />);
    expect($(".pe-avatar-preview img")).toBeTruthy();

    fireEvent.click($("#pe-avatar-remove")!);
    expect($(".pe-avatar-preview img")).toBeNull();
    fireEvent.click($("#pe-save")!);

    await waitFor(() => expect(holder.client.setProfile).toHaveBeenCalled());
    expect(holder.client.setProfile.mock.calls[0][0].avatar).toBeUndefined();
    await waitFor(() => expect(holder.dav.remove).toHaveBeenCalledWith(`.poweur/public/${old}`));
    expect(readLocalAvatar(ME)).toBeNull();
  });

  it("refuses a file that is not an image", async () => {
    render(<ProfileEditor profile={null} />);
    fireEvent.change($("#pe-avatar")!, { target: { files: [new File(["hello"], "notes.txt", { type: "text/plain" })] } });
    await waitFor(() => expect($("#pe-status")!.textContent).toBe("Choose an image file for your photo"));
    expect($(".pe-avatar-preview img")).toBeNull();
  });
});

describe("ProfileEditor identity page settings", () => {
  it("defaults the page and indexing on while anonymous messaging stays off", async () => {
    render(<ProfileEditor profile={null} />);

    expect($("#pe-page-enabled")).toBeChecked();
    expect($("#pe-page-indexable")).toBeChecked();
    expect($("#pe-page-anonymous")).not.toBeChecked();

    fireEvent.click($("#pe-page-enabled")!);
    fireEvent.click($("#pe-page-indexable")!);
    fireEvent.click($("#pe-page-anonymous")!);
    fireEvent.click($("#pe-save")!);

    await waitFor(() => expect(holder.client.setProfile).toHaveBeenCalled());
    expect(holder.client.setProfile.mock.calls[0][0].identity_page).toEqual({
      enabled: false,
      indexable: false,
      advertise_anonymous_messages: true,
    });
  });
});
