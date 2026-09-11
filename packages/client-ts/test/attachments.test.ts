import { describe, expect, it } from "vitest";
import { attachmentMetadata, MAX_ATTACHMENT_BYTES, parseAttachmentMetadata, uploadAttachment } from "../src/attachments.js";

describe("attachment references", () => {
  it("round trips the signed metadata convention", () => {
    const ref = { owner: "alice.example.org", path: "shared/.attachments/att_1/photo.jpg",
      name: "photo.jpg", size: 123, mime: "image/jpeg",
      sha256: "0123456789abcdef".repeat(4), shareId: "shr_1" };
    expect(parseAttachmentMetadata(attachmentMetadata(ref))).toEqual(ref);
  });

  it("refuses files over the 20 MB product limit before DAV", async () => {
    await expect(uploadAttachment({} as never, { identity: "alice.example.org" } as never,
      "bob.example.org", new Uint8Array(MAX_ATTACHMENT_BYTES + 1), { name: "huge.bin" }))
      .rejects.toThrow("20 MB");
  });

  it("rejects path traversal and mismatched display names", () => {
    const metadata = attachmentMetadata({
      owner: "alice.example.org", path: "shared/.attachments/att_1/photo.jpg",
      name: "photo.jpg", size: 1, mime: "image/jpeg",
      sha256: "0123456789abcdef".repeat(4), shareId: "shr_1",
    });
    expect(() => parseAttachmentMetadata({ ...metadata, attachment_path: "shared/.attachments/../photo.jpg" })).toThrow();
    expect(() => parseAttachmentMetadata({ ...metadata, attachment_name: "other.jpg" })).toThrow();
  });
});
