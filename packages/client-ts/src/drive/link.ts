/**
 * Opening a drive link (E20-T7): `https://<owner>/s/<link>#<secret>`.
 * Everything that decrypts happens here, in the viewer; the relay sees the
 * link ID and, for a password-protected link, the verifier half of the
 * argon2id output — never the fragment, the password or a key.
 */
import { argon2id } from "@noble/hashes/argon2.js";
import type { Signer } from "../crypto/keys.js";
import { fromBase64, utf8 } from "../encoding.js";
import { RelayClient } from "../http.js";
import { resolveSigningKey, type ResolveOptions } from "../resolve.js";
import { DriveClient } from "./client.js";
import { DriveFiles, linkSecret, type OpenFile } from "./files.js";

export interface LinkInfo {
  drive: string; role: string; expires?: string; kdf?: string; salt?: string; pow?: number;
  password: boolean; remaining_opens?: number;
}
export class LinkPasswordRequired extends Error {
  constructor() { super("this link needs a password"); }
}
export interface OpenedLink { info: LinkInfo; files: DriveFiles; root: OpenFile }

/** Split a link URL (or its path and fragment) into drive, link ID and secret. */
export function parseLinkUrl(href: string): { drive: string; link: string; fragment: Uint8Array } {
  const url = new URL(href);
  const match = /^\/s\/([0-9a-f]{32})\/?$/.exec(url.pathname);
  const fragment = fromBase64(url.hash.replace(/^#/, ""));
  if (!match || fragment.length !== 32) throw new Error("not a drive link");
  return { drive: url.hostname.toLowerCase(), link: match[1]!, fragment };
}

/** A link holder signs nothing; reads need no identity. The name is
 * reserved (RFC 2606), so it can never be mistaken for a version's author. */
function readOnlySigner(): Signer {
  return { identity: "link-holder.invalid", publicKey: "", async sign() { throw new Error("a link holder does not sign"); } };
}

/**
 * Open a link: learn its parameters, derive the key (and verifier) from the
 * fragment and password, and open the shared node. Opening counts against the
 * link's download cap. `origin` is the relay serving the owner's drive.
 */
export async function openLink(options: {
  origin: string; drive: string; link: string; fragment: Uint8Array; password?: string;
  resolve?: ResolveOptions; fetch?: typeof fetch;
}): Promise<OpenedLink> {
  const relay = new RelayClient(options.origin, options.fetch ? { fetch: options.fetch } : {});
  // Follows the owner's redirect when the shared subtree moved drives.
  const info = await relay.request<LinkInfo>({ method: "GET", path: `/drive/${encodeURIComponent(options.drive)}/links/${options.link}` });
  let secret = options.fragment, verifier: Uint8Array | undefined;
  if (info.password) {
    if (!options.password) throw new LinkPasswordRequired();
    if (!info.salt) throw new Error("link has no password salt");
    const derived = argon2id(utf8(options.password), fromBase64(info.salt), { t: 3, m: 65536, p: 1, dkLen: 64 });
    secret = linkSecret(options.fragment, derived.subarray(0, 32));
    verifier = derived.subarray(32);
  }
  const client = new DriveClient(relay, readOnlySigner(), info.drive);
  client.link = { id: options.link, ...(verifier ? { verifier } : {}) };
  const files = new DriveFiles(client, {
    async sign() { throw new Error("a link holder does not sign"); },
    encryptionPrivateKey: secret,
    async authorKey(author: string) {
      const key = await resolveSigningKey(author, options.resolve ?? {});
      if (!key) throw new Error(`cannot verify ${author}`);
      return fromBase64(key);
    },
  });
  const [root] = await files.shared();
  if (!root) throw new Error("the link opens nothing");
  return { info, files, root };
}
