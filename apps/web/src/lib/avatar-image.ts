/**
 * Turning a picked photo into an avatar: centre-cropped to a square and scaled
 * down, so every circle in the app shows the same framing and a phone photo
 * does not upload megabytes into `/public`. Where the browser cannot decode or
 * draw it, the original file is kept — the circle still crops it on screen.
 */
export const AVATAR_SIZE = 256;
export const MAX_AVATAR_BYTES = 15 * 1024 * 1024;

const EXTENSIONS: Record<string, string> = {
  "image/jpeg": "jpg",
  "image/png": "png",
  "image/webp": "webp",
  "image/gif": "gif",
};

/** A type every browser can show from someone else's public avatar URL. */
export const isWebImageType = (type: string): boolean => type in EXTENSIONS;

export const extensionFor = (type: string): string => EXTENSIONS[type] ?? "jpg";

export async function squareAvatar(file: Blob, size = AVATAR_SIZE): Promise<Blob> {
  if (typeof createImageBitmap !== "function" || typeof document === "undefined") return file;
  let bitmap: ImageBitmap | null = null;
  try {
    bitmap = await createImageBitmap(file);
    const side = Math.min(bitmap.width, bitmap.height);
    if (!side) return file;
    const canvas = document.createElement("canvas");
    canvas.width = size;
    canvas.height = size;
    const context = canvas.getContext("2d");
    if (!context) return file;
    // JPEG has no transparency: a transparent PNG lands on white, not black.
    context.fillStyle = "#fff";
    context.fillRect(0, 0, size, size);
    context.drawImage(bitmap, (bitmap.width - side) / 2, (bitmap.height - side) / 2, side, side, 0, 0, size, size);
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.88));
    return blob && blob.size > 0 ? blob : file;
  } catch {
    return file;
  } finally {
    bitmap?.close();
  }
}

export function blobToDataUrl(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error ?? new Error("Could not read the image"));
    reader.readAsDataURL(blob);
  });
}

/**
 * A short tag from the content, for the file name: a new photo gets a new URL,
 * so nobody's browser keeps showing the old one from cache.
 */
export async function contentTag(blob: Blob): Promise<string> {
  try {
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", await blob.arrayBuffer()));
    return [...digest.slice(0, 4)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  } catch {
    return Date.now().toString(36);
  }
}
