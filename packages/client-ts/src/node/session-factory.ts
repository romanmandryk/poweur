/**
 * One call that goes from "whatever is in ~/.poweur" to a ready client —
 * the same resolution order the Go CLI uses, so a script and the CLI always
 * agree about which identity is active.
 */

import { PoweurClient } from "../client.js";
import { signerFor, type StoredIdentityKeys } from "../crypto/keys.js";
import { PoweurError } from "../errors.js";
import type { ResolveOptions } from "../resolve.js";
import { loadConfig, type PoweurConfig } from "./config.js";
import { dialFetch } from "./dialfetch.js";
import { nodeTxtResolver } from "./dns.js";
import { FileKeyStore } from "./keystore.js";
import { FileSessionStore } from "./sessionstore.js";

export interface OpenOptions {
  /** Override the active identity (the CLI's `--use-identity`). */
  identity?: string;
  /** Override the relay URL (the CLI's `--relay`). */
  relayUrl?: string;
  resolve?: Partial<ResolveOptions>;
}

export interface OpenedClient {
  client: PoweurClient;
  config: PoweurConfig;
  keys: StoredIdentityKeys;
  keyStore: FileKeyStore;
}

/**
 * Resolver defaults matching the Go CLI's env switches, so local integration
 * runs behave identically from either client:
 *   POWEUR_RESOLVER_SCHEME=http     — resolve over plain HTTP
 *   RESOLVER_ALLOW_PRIVATE=1        — allow loopback targets (disables the SSRF guard)
 *   POWEUR_RESOLVER_DIAL=host:port  — send every resolver fetch to this address,
 *                                     keeping the identity as the Host header
 *   DNS_SERVER=host[:port]          — route DNS through a specific server
 */
export function nodeResolveOptions(overrides: Partial<ResolveOptions> = {}): ResolveOptions {
  const dial = (process.env["POWEUR_RESOLVER_DIAL"] ?? "").trim();
  return {
    scheme: process.env["POWEUR_RESOLVER_SCHEME"] === "http" ? "http" : "https",
    allowPrivate: process.env["RESOLVER_ALLOW_PRIVATE"] === "1",
    txt: nodeTxtResolver(),
    ...(dial ? { fetch: dialFetch({ dial }) } : {}),
    ...overrides,
  };
}

/** Open the identity configured in `~/.poweur`, ready to send and receive. */
export async function openClient(options: OpenOptions = {}): Promise<OpenedClient> {
  const config = loadConfig();
  const identity = options.identity || config.identity;
  if (!identity) {
    throw new PoweurError(
      "invalid_argument",
      "no identity configured (run `poweur identity create` or pass an identity)",
    );
  }
  const relayUrl = options.relayUrl || config.relay_url;
  if (!relayUrl) {
    throw new PoweurError("invalid_argument", "relay url not configured");
  }

  const keyStore = new FileKeyStore(config.keys_dir);
  const keys = await keyStore.load(identity);
  if (!keys) {
    throw new PoweurError("not_found", `no key found for ${identity} in ${config.keys_dir}`);
  }
  const { signer, decryptor } = signerFor(keys);

  const client = new PoweurClient({
    relayUrl,
    signer,
    decryptor,
    sessionStore: new FileSessionStore(),
    resolve: nodeResolveOptions(options.resolve ?? {}),
  });
  return { client, config, keys, keyStore };
}
