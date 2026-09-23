/**
 * `poweur key enroll` / `key approve` / `key claim` — pairing v2 (EPIC-011
 * E11-T8). Same flow, flags and ~/.poweur/pairing files as the Go CLI
 * (apps/cli/internal/cli/enroll.go), so either CLI can finish what the other
 * started. There is no way to approve without the pairing link or the digits.
 */

import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import {
  EnrollApi,
  formatShortCode,
  normalizeShortCode,
  pairingAppLink,
  pairingLink,
  parsePairingLink,
  type ApproverSession,
  type EnrollSession,
} from "../../enroll.js";
import { identityKeysFromSeed, signerFor } from "../../crypto/keys.js";
import { fromBase64, toBase64url } from "../../encoding.js";
import { RelayClient } from "../../http.js";
import { parseSeedOrMnemonic } from "../../kit.js";
import { loadConfig, saveConfig } from "../../node/config.js";
import { FileKeyStore } from "../../node/keystore.js";
import { encryptionKeyPath, poweurHome, signingKeyPath } from "../../node/paths.js";
import { flagBool, flagString, parseArgs, requirePositional, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";

const POLL_MS = 2000;
const TTL_MS = 10 * 60 * 1000;

function enrollApi(relayUrl: string): EnrollApi {
  return new EnrollApi(new RelayClient(relayUrl));
}

/** Same shape as the Go CLI's newDevicePairing. */
interface NewDeviceFile {
  identity: string;
  relay: string;
  code: string;
  link: string;
  claim_token: string;
  ephemeral_private_key: string;
  ephemeral_public_key: string;
  commit_nonce: string;
  commitment: string;
  expires_at: string;
  revealed?: boolean;
}

interface ApproverFile {
  identity: string;
  code: string;
  approver_nonce: string;
  commitment?: string;
}

function pairingPath(kind: string, identity: string, code: string): string {
  return join(poweurHome(), "pairing", `${kind}-${identity.toLowerCase()}-${code}.json`);
}

function savePairing(kind: string, identity: string, code: string, value: unknown): void {
  const path = pairingPath(kind, identity, code);
  mkdirSync(join(poweurHome(), "pairing"), { recursive: true, mode: 0o700 });
  writeFileSync(path, JSON.stringify(value), { mode: 0o600 });
}

function loadPairing<T>(kind: string, identity: string, code: string): T | null {
  try {
    return JSON.parse(readFileSync(pairingPath(kind, identity, code), "utf8")) as T;
  } catch {
    return null;
  }
}

function dropPairing(kind: string, identity: string, code: string): void {
  rmSync(pairingPath(kind, identity, code), { force: true });
}

function toSession(f: NewDeviceFile): EnrollSession {
  return {
    rendezvousId: f.code,
    claimToken: f.claim_token,
    commitment: f.commitment,
    expiresAt: f.expires_at,
    ephemeralPrivateKey: fromBase64(f.ephemeral_private_key),
    ephemeralPublicKey: f.ephemeral_public_key,
    commitNonce: f.commit_nonce,
    revealed: Boolean(f.revealed),
  };
}

function appUrlFor(relayUrl: string, identity: string): string {
  return relayUrl.startsWith("https://") ? `https://${identity.toLowerCase()}/app/` : `${relayUrl.replace(/\/+$/, "")}/app/`;
}

/** One step on the new device; returns output when it is done or has news. */
async function advance(file: NewDeviceFile): Promise<{ status: string; sas?: string; seed?: Uint8Array }> {
  const session = toSession(file);
  const step = await enrollApi(file.relay).step(file.identity, session);
  if (session.revealed && !file.revealed) {
    file.revealed = true;
    savePairing("new", file.identity, file.code, file);
  }
  switch (step.state) {
    case "offered":
      return { status: "Waiting for the other device…" };
    case "scan":
      return { status: "Approve on the other device." };
    case "compare":
      return { status: `Check the other device shows ${step.sas.slice(0, 3)} ${step.sas.slice(3)}, then approve there.`, sas: step.sas };
    case "delivered":
      return { status: "", seed: step.seed };
  }
}

/** Save the keys a delivered seed derives — only if they are the identity's published keys. */
async function adopt(file: NewDeviceFile, seed: Uint8Array, streams: Streams, jsonOut: boolean): Promise<number> {
  const keys = identityKeysFromSeed(file.identity, seed);
  if (!(await enrollApi(file.relay).publishedKeyMatches(file.identity, seed))) {
    throw new UsageError(`the keys received are not ${file.identity}'s published keys — not saved`);
  }
  const config = loadConfig();
  const keyStore = new FileKeyStore(config.keys_dir);
  await keyStore.save(keys);
  saveConfig({ ...config, identity: file.identity, relay_url: file.relay, keys_dir: keyStore.keysDir });
  dropPairing("new", file.identity, file.code);
  return write(
    streams,
    jsonOut,
    {
      identity: file.identity,
      key_path: signingKeyPath(keyStore.keysDir, file.identity),
      encryption_key_path: encryptionKeyPath(keyStore.keysDir, file.identity),
      enrolled: true,
    },
    `enrolled ${file.identity} on this device\n`,
  );
}

export async function keyEnroll(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { bool: ["json", "wait"], defaults: { relay: config.relay_url } });
  const identity = requirePositional(args, 0, "usage: poweur key enroll <identity> [--relay ...] [--wait]").toLowerCase();
  const relayUrl = flagString(args, "relay")?.replace(/\/+$/, "");
  if (!relayUrl) throw new UsageError("--relay is required (this machine has no configuration yet)");
  const jsonOut = flagBool(args, "json");

  const session = await enrollApi(relayUrl).offer(identity, flagString(args, "label") || undefined);
  const file: NewDeviceFile = {
    identity,
    relay: relayUrl,
    code: session.rendezvousId,
    link: pairingLink(appUrlFor(relayUrl, identity), identity, session.rendezvousId, session.commitment),
    claim_token: session.claimToken,
    ephemeral_private_key: toBase64url(session.ephemeralPrivateKey),
    ephemeral_public_key: session.ephemeralPublicKey,
    commit_nonce: session.commitNonce,
    commitment: session.commitment,
    expires_at: session.expiresAt,
  };
  savePairing("new", identity, file.code, file);
  const appLink = pairingAppLink(identity, file.code, file.commitment);
  if (!jsonOut) {
    streams.stdout(
      `Approve on a device that already has ${identity}.\n\n` +
        `Poweur app — open:  ${appLink}\n` +
        `Browser — open:     ${file.link}\n` +
        `Terminal — run:     poweur key approve '${file.link}' --seed …\n` +
        `Or enter the code ${formatShortCode(file.code)} there (Settings → Keys & devices → Add a device).\n\n`,
    );
  }
  if (!flagBool(args, "wait")) {
    if (!jsonOut) streams.stdout(`Then finish here:  poweur key claim ${identity} ${file.code}\n`);
    return write(streams, jsonOut, { identity, code: file.code, link: file.link, app_link: appLink, expires_at: file.expires_at }, "");
  }
  const deadline = Date.now() + TTL_MS;
  let shown = "";
  while (Date.now() < deadline) {
    const out = await advance(file);
    if (out.seed) return adopt(file, out.seed, streams, jsonOut);
    if (out.status !== shown && !jsonOut) streams.stdout(`${out.status}\n`);
    shown = out.status;
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
  }
  throw new UsageError("timed out waiting for approval");
}

export async function keyClaim(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: ["json"] });
  const identity = requirePositional(args, 0, "usage: poweur key claim <identity> <code>").toLowerCase();
  const code = normalizeShortCode(requirePositional(args, 1, "the pairing code is required"));
  if (!code) throw new UsageError("that is not a pairing code");
  const file = loadPairing<NewDeviceFile>("new", identity, code);
  if (!file) throw new UsageError(`no pairing ${code} for ${identity} on this machine — start one with \`poweur key enroll\``);
  const out = await advance(file);
  if (out.seed) return adopt(file, out.seed, streams, flagBool(args, "json"));
  return write(
    streams,
    flagBool(args, "json"),
    { identity, code, enrolled: false, status: out.status, ...(out.sas ? { sas: out.sas } : {}) },
    `${out.status}\n`,
  );
}

export async function keyApprove(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, {
    bool: ["json", "no-wait"],
    defaults: { relay: config.relay_url, "use-identity": config.identity },
  });
  const input = requirePositional(args, 0, "usage: poweur key approve <pairing-link | code> [--sas 123456]");
  const identity = flagString(args, "use-identity")?.toLowerCase();
  const relayUrl = flagString(args, "relay");
  if (!identity || !relayUrl) throw new UsageError("identity and relay url required");
  const seedFlag = flagString(args, "seed");
  if (!seedFlag) throw new UsageError("--seed is required: the seed lives only on your devices, never on the relay");
  const seed = parseSeedOrMnemonic(seedFlag);
  const { signer } = signerFor(identityKeysFromSeed(identity, seed));
  const api = enrollApi(relayUrl);
  const jsonOut = flagBool(args, "json");

  const link = parsePairingLink(input);
  const code = link?.code ?? normalizeShortCode(input);
  if (!code) throw new UsageError("that is neither a pairing link nor a code (8 characters, like K7QM-4XP2)");
  // Keep this side's nonce across runs, or the digits would change.
  const saved = loadPairing<ApproverFile>("approve", identity, code);
  let session: ApproverSession;
  if (saved) {
    session = {
      code,
      mode: link ? "scan" : "compare",
      approverNonce: saved.approver_nonce,
      commitment: link?.commitment ?? saved.commitment ?? "",
    };
  } else {
    session = await api.begin(signer, identity, input);
    savePairing("approve", identity, code, { identity, code, approver_nonce: session.approverNonce, commitment: session.commitment });
  }

  const deadline = Date.now() + TTL_MS;
  let step = await api.wait(signer, identity, session);
  while (step.state === "waiting") {
    if (flagBool(args, "no-wait") || Date.now() > deadline) {
      return write(
        streams,
        jsonOut,
        { identity, code, approved: false, status: "waiting for the new device" },
        "Waiting for the new device — run this again once it shows its digits.\n",
      );
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    step = await api.wait(signer, identity, session);
  }
  if (session.mode === "compare") {
    const given = (flagString(args, "sas") ?? "").replace(/\s+/g, "");
    if (!given) {
      throw new UsageError(
        "the new device shows six digits; run again with --sas <digits> to approve " +
          "(this side computed them independently, and delivers only if they match)",
      );
    }
    if (given !== step.sas) {
      dropPairing("approve", identity, code);
      throw new UsageError(
        `the new device's digits do not match this one's (${step.sas.slice(0, 3)} ${step.sas.slice(3)}) — not approved. ` +
          "Another device may be trying to pair; start again",
      );
    }
  }
  await api.approve(signer, identity, session, step, seed);
  dropPairing("approve", identity, code);
  return write(streams, jsonOut, { identity, code, mode: session.mode, approved: true }, `approved the new device for ${identity}\n`);
}

export async function keyCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const rest = argv.slice(1);
  switch (sub) {
    case "rotate": {
      const { keyRotate } = await import("./identity.js");
      return keyRotate(rest, streams);
    }
    case "enroll":
      return keyEnroll(rest, streams);
    case "approve":
      return keyApprove(rest, streams);
    case "claim":
      return keyClaim(rest, streams);
    default:
      throw new UsageError("usage: poweur key <rotate|enroll|approve|claim> …");
  }
}
