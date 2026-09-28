/**
 * `.poweur/public/profile.json` — the twin of `packages/identity/profile.go`.
 *
 * World-readable self-description: it is what `/.well-known/poweur/profile.json`
 * serves and what every client renders when it shows a person. Owner-written and
 * relay-validated, so this validator exists to fail *before* the write, with a
 * message about the field rather than a 400 about the document.
 *
 * The avatar rule is the one worth knowing: it names an image file in the
 * identity's own `.poweur/public/` (served at `/.well-known/poweur/<name>`),
 * never an external URL, so rendering someone's profile cannot be turned into a
 * request to a third-party host.
 */

import { PoweurError } from "./errors.js";
import type { SystemFiles } from "./systemfiles.js";
import type { Profile } from "./types.js";

export const PROFILE_PATH = ".poweur/public/profile.json";

/** Image types an avatar may have (identity.ValidAvatarName). */
export const AVATAR_EXTENSIONS = [".png", ".jpg", ".jpeg", ".webp", ".gif"] as const;

/** Is name a flat image file name in `.poweur/public/` (identity.ValidAvatarName)? */
export function validAvatarName(name: string): boolean {
  if (!name || name.length > 64) return false;
  const dot = name.indexOf(".");
  if (dot <= 0 || name.indexOf(".", dot + 1) !== -1) return false;
  if (!/^[a-z0-9_-]+$/.test(name.slice(0, dot))) return false;
  return (AVATAR_EXTENSIONS as readonly string[]).includes(name.slice(dot));
}

/** The URL an avatar file is served at (identity.AvatarURL); "" when invalid. */
export function avatarUrl(identity: string, avatar: string | undefined, scheme = "https"): string {
  const name = (avatar ?? "").trim();
  if (!validAvatarName(name)) return "";
  return `${scheme}://${identity.trim().toLowerCase()}/.well-known/poweur/${name}`;
}
export const MAX_PROFILE_LINKS = 32;
export const MAX_DISPLAY_NAME = 256;
export const MAX_BIO = 4096;

export function emptyProfile(): Profile {
  return { version: 1 };
}

/** Structural validation mirroring `Profile.Validate`. */
export function validateProfile(profile: Profile): void {
  if (profile.version !== 0 && profile.version !== 1) {
    throw new PoweurError("invalid_document", `unsupported profile version ${profile.version}`);
  }
  if ((profile.display_name ?? "").length > MAX_DISPLAY_NAME) {
    throw new PoweurError("invalid_document", `display_name too long (max ${MAX_DISPLAY_NAME})`);
  }
  if ((profile.bio ?? "").length > MAX_BIO) {
    throw new PoweurError("invalid_document", `bio too long (max ${MAX_BIO})`);
  }
  if (profile.avatar && !validAvatarName(profile.avatar)) {
    throw new PoweurError(
      "invalid_document",
      `avatar must be an image file name like avatar.png (got "${profile.avatar}")`,
    );
  }
  const links = profile.links ?? [];
  if (links.length > MAX_PROFILE_LINKS) {
    throw new PoweurError("invalid_document", `too many links (max ${MAX_PROFILE_LINKS})`);
  }
  links.forEach((link, index) => {
    if (!link.url?.trim()) {
      throw new PoweurError("invalid_document", `link ${index}: url is required`);
    }
  });
}

/** Read our own profile document (an absent one is an empty profile). */
export async function readProfile(files: SystemFiles): Promise<{ profile: Profile; explicit: boolean }> {
  const raw = await files.readOptional(PROFILE_PATH);
  if (!raw) return { profile: emptyProfile(), explicit: false };
  const profile = JSON.parse(raw) as Profile;
  validateProfile(profile);
  return { profile, explicit: true };
}

/** Write it, dropping empty fields so the document says only what is set. */
export async function writeProfile(files: SystemFiles, profile: Profile): Promise<Profile> {
  const document: Profile = { version: 1 };
  if (profile.display_name?.trim()) document.display_name = profile.display_name.trim();
  if (profile.avatar?.trim()) document.avatar = profile.avatar.trim();
  if (profile.bio?.trim()) document.bio = profile.bio.trim();
  if (profile.locale?.trim()) document.locale = profile.locale.trim();
  const links = (profile.links ?? []).filter((link) => link.url?.trim());
  if (links.length) {
    document.links = links.map((link) => ({
      ...(link.label?.trim() ? { label: link.label.trim() } : {}),
      url: link.url.trim(),
    })) as Profile["links"];
  }
  validateProfile(document);
  await files.writeJson(PROFILE_PATH, document);
  return document;
}
