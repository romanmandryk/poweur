import { describe, expect, it, vi } from "vitest";
import { Contacts } from "../src/contacts.js";
import type { SystemFiles } from "../src/systemfiles.js";
import type { Contact, ContactsFile } from "../src/types.js";

const key = "ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo";
function fixture(contacts: Contact[]) {
  let file: ContactsFile = { version: 1, contacts };
  const files = {
    readOptional: vi.fn(async () => JSON.stringify(file)),
    writeJson: vi.fn(async (_path: string, next: ContactsFile) => { file = next; }),
  };
  const api = new Contacts(files as unknown as SystemFiles);
  vi.spyOn(api, "resolvePin").mockResolvedValue(key);
  return { api, files };
}

describe("automatic contact acceptance", () => {
  it("promotes only requested contacts, once, preserving metadata and the pin", async () => {
    const { api, files } = fixture([
      { identity: "bob.example", state: "requested", pinned_key: key, petname: "Bob", tags: ["friend"], added_at: "2026-09-01T00:00:00Z" },
      { identity: "blocked.example", state: "blocked", pinned_key: key },
      { identity: "accepted.example", state: "accepted", pinned_key: key },
    ]);
    expect(await api.promoteAccepted(["BOB.EXAMPLE", "bob.example", "blocked.example", "accepted.example", "stranger.example"])).toEqual(["bob.example"]);
    expect((await api.load()).contacts[0]).toMatchObject({ state: "accepted", pinned_key: key, petname: "Bob", tags: ["friend"], added_at: "2026-09-01T00:00:00Z" });
    expect(await api.promoteAccepted(["bob.example"])).toEqual([]);
    expect(files.writeJson).toHaveBeenCalledTimes(1);
  });

  it.each(["changed key", "resolution failure"])("leaves the request pending on %s", async (failure) => {
    const { api, files } = fixture([{ identity: "bob.example", state: "requested", pinned_key: key }]);
    if (failure === "changed key") vi.mocked(api.resolvePin).mockResolvedValue("ed25519:another-key");
    else vi.mocked(api.resolvePin).mockRejectedValue(new Error("offline"));
    expect(await api.promoteAccepted(["bob.example"])).toEqual([]);
    expect(files.writeJson).not.toHaveBeenCalled();
    expect((await api.load()).contacts[0]?.state).toBe("requested");
  });

  it("propagates write failure so archived answers can be retried", async () => {
    const { api, files } = fixture([{ identity: "bob.example", state: "requested", pinned_key: key }]);
    files.writeJson.mockRejectedValueOnce(new Error("offline"));
    await expect(api.promoteAccepted(["bob.example"])).rejects.toThrow("offline");
    expect(await api.promoteAccepted(["bob.example"])).toEqual(["bob.example"]);
  });
});
