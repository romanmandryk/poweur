import { describe, expect, it } from "vitest";
import { contentTag, extensionFor, isWebImageType, squareAvatar } from "../../src/lib/avatar-image";

describe("avatar images", () => {
  it("names the file after what it is", () => {
    expect(extensionFor("image/jpeg")).toBe("jpg");
    expect(extensionFor("image/png")).toBe("png");
    expect(extensionFor("image/webp")).toBe("webp");
    expect(isWebImageType("image/gif")).toBe(true);
    expect(isWebImageType("image/heic")).toBe(false);
  });

  it("tags content so a new photo gets a new URL", async () => {
    const one = await contentTag(new Blob(["one photo"]));
    expect(one).toMatch(/^[0-9a-f]{8}$/);
    expect(await contentTag(new Blob(["one photo"]))).toBe(one);
    expect(await contentTag(new Blob(["another photo"]))).not.toBe(one);
  });

  it("keeps the original where the browser cannot draw it", async () => {
    const file = new File(["not decodable here"], "me.png", { type: "image/png" });
    expect(await squareAvatar(file)).toBe(file);
  });
});
