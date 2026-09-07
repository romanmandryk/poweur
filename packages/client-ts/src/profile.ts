/**
 * `poweur-sys/public/profile.json` — the twin of `packages/identity/profile.go`.
 *
 * World-readable self-description: it is what `/.well-known/poweur/profile.json`
 * serves and what every client renders when it shows a person. Owner-written and
 * relay-validated, so this validator exists to fail *before* the write, with a
 * message about the field rather than a 400 about the document.
 *
 * The avatar rule is the one worth knowing: it is a path into the identity's own
 * `/public` tree, never an external URL, so rendering someone's profile cannot be
 * turned into a request to a third-party host.
 */

import { PoweurError } from "./errors.js";
import type { DavClient } from "./files.js";
import type { Profile } from "./types.js";

export const PROFILE_PATH = "poweur-sys/public/profile.json";
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
  if (profile.avatar && !profile.avatar.startsWith("public/")) {
    throw new PoweurError(
      "invalid_document",
      `avatar must be a path under public/ (got "${profile.avatar}")`,
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
export async function readProfile(dav: DavClient): Promise<{ profile: Profile; explicit: boolean }> {
  const raw = await dav.readOptional(PROFILE_PATH);
  if (!raw) return { profile: emptyProfile(), explicit: false };
  const profile = JSON.parse(raw) as Profile;
  validateProfile(profile);
  return { profile, explicit: true };
}

/** Write it, dropping empty fields so the document says only what is set. */
export async function writeProfile(dav: DavClient, profile: Profile): Promise<Profile> {
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
  await dav.writeJson(PROFILE_PATH, document);
  return document;
}
