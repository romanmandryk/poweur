import { describe, expect, it, vi } from "vitest";
import {
  advertisesAnonymousMessages,
  identityPageEnabled,
  identityPageIndexable,
  validateProfile,
  writeProfile,
} from "../src/profile.js";

describe("identity page profile settings", () => {
  it("defaults existing profiles to an enabled, indexable page without anonymous advertising", () => {
    const profile = { version: 1 };
    expect(identityPageEnabled(profile)).toBe(true);
    expect(identityPageIndexable(profile)).toBe(true);
    expect(advertisesAnonymousMessages(profile)).toBe(false);
  });

  it("honours explicit public presentation choices", () => {
    const profile = {
      version: 1,
      identity_page: { enabled: false, indexable: false, advertise_anonymous_messages: true },
    };
    expect(identityPageEnabled(profile)).toBe(false);
    expect(identityPageIndexable(profile)).toBe(false);
    expect(advertisesAnonymousMessages(profile)).toBe(true);
    expect(() => validateProfile(profile)).not.toThrow();
  });

  it("rejects non-boolean page fields from untyped JSON", () => {
    expect(() => validateProfile({ version: 1, identity_page: { enabled: "no" } } as never)).toThrow(
      "identity_page.enabled must be a boolean",
    );
  });

  it("preserves explicit false values when writing", async () => {
    const files = { writeJson: vi.fn(async () => null) };
    const written = await writeProfile(files as never, {
      version: 1,
      display_name: " Alice ",
      identity_page: { enabled: false, indexable: true, advertise_anonymous_messages: false },
    });
    expect(written.identity_page).toEqual({ enabled: false, indexable: true, advertise_anonymous_messages: false });
    expect(files.writeJson).toHaveBeenCalledWith(".poweur/public/profile.json", written);
  });
});
