/** `poweur identity …` and `poweur key rotate`. */

import { existsSync, renameSync, writeFileSync } from "node:fs";
import { promises as dns } from "node:dns";

import { canonicalIdentityRotation } from "../../canonical.js";
import { ed25519PublicKey, newSeed, signBytes, x25519PublicKey } from "../../crypto/index.js";
import { identityKeysFromSeed, LocalSigner } from "../../crypto/keys.js";
import { newRecoveryKit, parseSeedOrMnemonic } from "../../kit.js";
import { newDocument, signDocumentWithKey } from "../../document.js";
import { rfc3339, toBase64Std, toBase64url } from "../../encoding.js";
import { newNonce } from "../../ids.js";
import { RelayClient, relayAddressFromUrl } from "../../http.js";
import { createIdentity, IdentityApi } from "../../identity.js";
import { resolveIdentity } from "../../resolve.js";
import { loadConfig, saveConfig } from "../../node/config.js";
import { nodeTxtResolver } from "../../node/dns.js";
import { FileKeyStore } from "../../node/keystore.js";
import { nodeResolveOptions } from "../../node/session-factory.js";
import { defaultKeysDir, encryptionKeyPath, signingKeyPath } from "../../node/paths.js";
import { flagBool, flagString, parseArgs, requirePositional, UsageError, type ParsedArgs } from "../args.js";
import { write, type Streams } from "../output.js";

const COMMON_BOOL = ["json"];

function activeIdentity(args: ParsedArgs, fallback: string): string {
  return flagString(args, "use-identity") || fallback;
}

export async function identityCreate(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, {
    bool: [...COMMON_BOOL, "hosted"],
    defaults: {
      "parent-domain": config.parent_domain,
      relay: config.relay_url,
    },
  });
  const raw = requirePositional(args, 0, "identity handle is required");
  const parentDomain = flagString(args, "parent-domain");
  let identity = raw;
  if (!raw.includes(".")) {
    if (!parentDomain) {
      throw new UsageError("parent domain is required when using a handle");
    }
    identity = `${raw}.${parentDomain.replace(/^\./, "")}`;
  }

  const relayUrl = flagString(args, "relay");
  const keysDir = config.keys_dir || defaultKeysDir();
  const keyStore = new FileKeyStore(keysDir);

  // Every identity is seed-based: keys come from --seed, or from a new seed
  // shown once below. They are generated and saved even without a relay, so
  // `identity create` offline still leaves usable material (the Go CLI does
  // the same).
  const seedFlag = flagString(args, "seed");
  const generated = !seedFlag;
  const seed = seedFlag ? parseSeedOrMnemonic(seedFlag) : newSeed();
  const keys = identityKeysFromSeed(identity, seed);

  let registered = false;
  let response: unknown = null;
  const hosted = flagBool(args, "hosted");
  if (relayUrl) {
    const api = new IdentityApi(new RelayClient(relayUrl));
    await api.health();
    const created = await createIdentity(api, identity, {
      hosted,
      keys,
      ...(flagString(args, "dns-provider") ? { dnsProvider: flagString(args, "dns-provider") } : {}),
      ...(resolveDnsToken(args) ? { dnsToken: resolveDnsToken(args) } : {}),
      ...(flagString(args, "invite-code") ? { inviteCode: flagString(args, "invite-code") } : {}),
    });
    registered = true;
    response = created.response;
  }

  await keyStore.save(keys);
  saveConfig({
    ...config,
    identity,
    keys_dir: keysDir,
    relay_url: relayUrl,
    parent_domain: parentDomain,
  });

  const payload = {
    identity,
    public_key: toBase64url(ed25519PublicKey(keys.signingPrivateKey)),
    encryption_public_key: toBase64url(x25519PublicKey(keys.encryptionPrivateKey!)),
    key_path: signingKeyPath(keysDir, identity),
    encryption_key_path: encryptionKeyPath(keysDir, identity),
    relay: relayUrl,
    registered,
    hosted,
    response,
    ...(generated ? seedFields(identity, seed, streams, flagBool(args, "json")) : {}),
  };
  const mode = hosted ? "hosted registration" : "registered with relay";
  const message = registered
    ? `created identity ${identity} (${mode}, e2e encryption enabled)\n`
    : `created identity ${identity} (local only; relay not configured)\n`;
  return write(streams, flagBool(args, "json"), payload, message);
}

/** Token precedence matches the Go CLI: flag, provider env, then DNS_TOKEN. */
function resolveDnsToken(args: ParsedArgs): string {
  const explicit = flagString(args, "dns-token");
  if (explicit) return explicit;
  const provider = (flagString(args, "dns-provider") || process.env["DNS_PROVIDER"] || "cloudflare").toLowerCase();
  if (provider === "cloudflare" && process.env["CLOUDFLARE_API_TOKEN"]) {
    return process.env["CLOUDFLARE_API_TOKEN"] as string;
  }
  if (provider === "hetzner" && process.env["HETZNER_API_TOKEN"]) {
    return process.env["HETZNER_API_TOKEN"] as string;
  }
  return process.env["DNS_TOKEN"] ?? "";
}

export async function identityShow(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const identity = activeIdentity(args, config.identity);
  if (!identity) throw new UsageError("no identity configured");
  const keyStore = new FileKeyStore(config.keys_dir);
  const keys = await keyStore.load(identity);
  if (!keys) throw new UsageError(`no key found for ${identity} in ${config.keys_dir}`);

  const payload: Record<string, string> = {
    identity,
    public_key: toBase64url(ed25519PublicKey(keys.signingPrivateKey)),
    key_path: signingKeyPath(config.keys_dir, identity),
  };
  if (keys.encryptionPrivateKey) {
    payload["encryption_public_key"] = toBase64url(x25519PublicKey(keys.encryptionPrivateKey));
    payload["encryption_key_path"] = encryptionKeyPath(config.keys_dir, identity);
  }
  return write(streams, flagBool(args, "json"), payload, `${identity}\n`);
}

export async function identityList(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const identities = await new FileKeyStore(config.keys_dir).list();
  if (flagBool(args, "json")) {
    return write(streams, true, { identities, active: config.identity }, "");
  }
  if (identities.length === 0) {
    streams.stdout("no identities found\n");
    return 0;
  }
  for (const identity of identities) {
    streams.stdout(`${identity === config.identity ? "* " : "  "}${identity}\n`);
  }
  return 0;
}

export async function identityUse(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const identity = requirePositional(args, 0, "usage: poweur identity use <identity>");
  const keyPath = signingKeyPath(config.keys_dir, identity);
  if (!existsSync(keyPath)) {
    throw new UsageError(`key not found for ${identity} at ${keyPath}`);
  }
  saveConfig({ ...config, identity });
  return write(
    streams,
    flagBool(args, "json"),
    { identity, key_path: keyPath },
    `active identity set to ${identity}\n`,
  );
}

export async function identityLookup(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const identity = requirePositional(args, 0, "usage: poweur identity lookup <identity>");
  const { document, source } = await resolveIdentity(identity, nodeResolveOptions());
  const payload = {
    identity: document.identity,
    source,
    public_key: document.public_key,
    encryption_public_key: document.encryption_public_key ?? "",
    relay: document.relay,
    capabilities: document.capabilities ?? [],
  };
  if (flagBool(args, "json")) return write(streams, true, payload, "");
  streams.stdout(
    `identity: ${payload.identity}\nsource: ${source}\npublic_key: ${payload.public_key}\n` +
      `encryption_public_key: ${payload.encryption_public_key}\nrelay: ${payload.relay}\n`,
  );
  return 0;
}

/** `identity dns` — the raw DNS view, for debugging a registration. */
export async function identityDns(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const identity = args.positional[0] || activeIdentity(args, config.identity);
  if (!identity) throw new UsageError("usage: poweur identity dns <identity>");

  const txt = nodeTxtResolver();
  const pick = async (name: string, prefix: string): Promise<string> => {
    try {
      return (await txt.lookupTxt(name)).map((r) => r.trim()).find((r) => r.startsWith(prefix)) ?? "";
    } catch {
      return "";
    }
  };
  const publicKeyTxt = await pick(`_poweur.${identity}`, "poweur-pubkey=");
  const encryptionKeyTxt = await pick(`_poweur-enc.${identity}`, "poweur-enckey=");
  let cname = "";
  try {
    cname = (await dns.resolveCname(identity))[0] ?? "";
  } catch {
    // No CNAME is the normal case for an apex or A-record identity.
  }
  let relayHosts: string[] = [];
  try {
    relayHosts = (await dns.lookup(identity, { all: true })).map((entry) => entry.address);
  } catch {
    // An unresolvable host is itself the answer the user is looking for.
  }

  const payload = {
    identity,
    public_key_txt: publicKeyTxt,
    encryption_key_txt: encryptionKeyTxt,
    relay_hosts: relayHosts,
    cname,
  };
  if (flagBool(args, "json")) return write(streams, true, payload, "");
  streams.stdout(`identity: ${identity}\n`);
  streams.stdout(`public key TXT: ${publicKeyTxt || "not found"}\n`);
  streams.stdout(`encryption key TXT: ${encryptionKeyTxt || "not found"}\n`);
  streams.stdout(`relay A/CNAME: ${relayHosts.length ? relayHosts.join(", ") : "not found"}\n`);
  if (cname) streams.stdout(`cname: ${cname}\n`);
  return 0;
}

export async function identityExport(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const identity = activeIdentity(args, config.identity);
  if (!identity) throw new UsageError("identity not configured");
  if (!config.relay_url) throw new UsageError("relay url not configured");
  const keys = await new FileKeyStore(config.keys_dir).load(identity);
  if (!keys) throw new UsageError(`no key found for ${identity}`);

  const api = new IdentityApi(new RelayClient(config.relay_url));
  const bytes = await api.export(new LocalSigner(identity, keys.signingPrivateKey));
  const outPath = flagString(args, "out") || `${identity}.tar.gz`;
  writeFileSync(outPath, bytes, { mode: 0o600 });
  streams.stdout(`exported ${outPath} (${bytes.length} bytes)\n`);
  return 0;
}

/**
 * The recovery kit for a seed this command just generated: in the JSON output,
 * and on stderr in human mode. It is the user's only copy.
 */
function seedFields(identity: string, seed: Uint8Array, streams: Streams, json: boolean) {
  const kit = newRecoveryKit(identity, "", seed);
  if (!json) {
    streams.stderr(
      `recovery kit for ${identity} — store this; it is the only way back\n` +
        `  seed:     ${kit.seed}\n  mnemonic: ${kit.mnemonic}\n`,
    );
  }
  return { seed: kit.seed, mnemonic: kit.mnemonic };
}

/**
 * `key rotate` — move the identity onto a new seed (new signing and encryption
 * keys), publish a document that lists the old signing key under
 * `previous_keys` for the grace period, and keep a backup of the old key files. Contacts who pinned the old key see a covered rotation rather than
 * a mismatch.
 */
export async function keyRotate(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { bool: COMMON_BOOL, defaults: { grace: "168h" } });
  const identity = activeIdentity(args, config.identity);
  if (!identity || !config.relay_url) throw new UsageError("identity and relay url required");

  const keyStore = new FileKeyStore(config.keys_dir);
  const keys = await keyStore.load(identity);
  if (!keys) throw new UsageError(`no key found for ${identity}`);

  const oldPublic = toBase64url(ed25519PublicKey(keys.signingPrivateKey));
  // Rotation moves the identity onto a new master seed: both long-lived keys
  // change together, so a recovery kit always covers everything.
  const seed = newSeed();
  const fresh = identityKeysFromSeed(identity, seed);
  const newPublic = toBase64url(ed25519PublicKey(fresh.signingPrivateKey));
  const encryptionPublic = toBase64url(x25519PublicKey(fresh.encryptionPrivateKey!));

  const issuedAt = rfc3339();
  const nonce = newNonce();
  const graceMs = parseDuration(flagString(args, "grace", "168h"));
  const validUntil = rfc3339(new Date(Date.now() + graceMs));

  let document = newDocument({
    identity,
    publicKey: newPublic,
    ...(encryptionPublic ? { encryptionPublicKey: encryptionPublic } : {}),
    relay: relayAddressFromUrl(config.relay_url),
    updatedAt: issuedAt,
  });
  document.previous_keys = [{ public_key: `ed25519:${oldPublic}`, valid_until: validUntil }];
  document = signDocumentWithKey(document, fresh.signingPrivateKey);

  const client = new RelayClient(config.relay_url);
  const rotationSignature = toBase64Std(
    signBytes(
      keys.signingPrivateKey,
      new TextEncoder().encode(
        canonicalIdentityRotation(identity, oldPublic, newPublic, issuedAt, nonce),
      ),
    ),
  );
  const response = await client.request<{ public_key: string }>({
    method: "POST",
    path: `/identities/${encodeURIComponent(identity)}/rotate`,
    body: {
      identity_document: document,
      new_public_key: newPublic,
      ...(encryptionPublic ? { encryption_public_key: encryptionPublic } : {}),
      issued_at: issuedAt,
      nonce,
      rotation_signature: rotationSignature,
    },
  });

  // Back up the old keys before overwriting: a rotation the relay accepted but
  // whose new keys never reached disk would lock the identity out, and the old
  // encryption key still opens mail sent to it.
  for (const oldPath of [signingKeyPath(config.keys_dir, identity), encryptionKeyPath(config.keys_dir, identity)]) {
    if (existsSync(oldPath)) renameSync(oldPath, `${oldPath}.pre-rotate`);
  }
  await keyStore.save(fresh);

  return write(
    streams,
    flagBool(args, "json"),
    {
      identity,
      public_key: response.public_key,
      previous_key: oldPublic,
      valid_until: validUntil,
      ...seedFields(identity, seed, streams, flagBool(args, "json")),
    },
    `rotated keys for ${identity} onto a new seed (old signing key valid until ${validUntil})\n`,
  );
}

/** Go-style duration strings (`168h`, `30m`, `1h30m`) in milliseconds. */
export function parseDuration(value: string): number {
  const matches = value.matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h)/g);
  const units: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000 };
  let total = 0;
  let matched = false;
  for (const match of matches) {
    matched = true;
    total += Number(match[1]) * (units[match[2] as string] as number);
  }
  if (!matched) throw new UsageError(`invalid duration "${value}"`);
  return total;
}

export async function identityCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const rest = argv.slice(1);
  switch (sub) {
    case "create": return identityCreate(rest, streams);
    case "show": return identityShow(rest, streams);
    case "dns": return identityDns(rest, streams);
    case "use": return identityUse(rest, streams);
    case "list": return identityList(rest, streams);
    case "lookup": return identityLookup(rest, streams);
    case "export": return identityExport(rest, streams);
    default:
      throw new UsageError(
        "unknown identity subcommand (want create, show, dns, use, list, lookup, export)",
      );
  }
}
