/** `poweur key enroll` / `key approve` / `key claim` — EPIC-011 E11-T3. */

import { EnrollApi, decodeEphemeralKey, normalizeRendezvousId } from "../../enroll.js";
import { identityKeysFromSeed, signerFor } from "../../crypto/keys.js";
import { toBase64url } from "../../encoding.js";
import { RelayClient } from "../../http.js";
import { parseSeedOrMnemonic } from "../../kit.js";
import { loadConfig, saveConfig } from "../../node/config.js";
import { FileKeyStore } from "../../node/keystore.js";
import { encryptionKeyPath, signingKeyPath } from "../../node/paths.js";
import { flagBool, flagString, parseArgs, requirePositional, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";

const JSON_FLAG = { bool: ["json"] };

function enrollApi(relayUrl: string): EnrollApi {
  return new EnrollApi(new RelayClient(relayUrl));
}

export async function keyEnroll(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, {
    bool: ["json", "wait"],
    defaults: { relay: config.relay_url },
  });
  const identity = requirePositional(args, 0, "usage: poweur key enroll <identity> [--relay ...] [--wait]");
  const relayUrl = flagString(args, "relay");
  if (!relayUrl) {
    throw new UsageError("--relay is required (this machine has no configuration yet)");
  }

  const api = enrollApi(relayUrl);
  const session = await api.offer(identity, flagString(args, "label") || undefined);
  const jsonOut = flagBool(args, "json");

  if (!jsonOut) {
    streams.stdout(
      `On a device that already has ${identity}, run:\n\n` +
        `  poweur key approve ${session.rendezvousId}\n\n` +
        `and confirm this code matches: ${session.sas}\n\n`,
    );
  }

  if (!flagBool(args, "wait")) {
    return write(
      streams,
      jsonOut,
      {
        rendezvous_id: session.rendezvousId,
        sas: session.sas,
        expires_at: session.expiresAt,
        ephemeral_private_key: toBase64url(session.ephemeralPrivateKey),
      },
      "",
    );
  }

  const deadline = Date.now() + 10 * 60 * 1000;
  while (Date.now() < deadline) {
    const seed = await api.claim(identity, session);
    if (seed) {
      const keyStore = new FileKeyStore(config.keys_dir);
      await keyStore.save(identityKeysFromSeed(identity, seed));
      saveConfig({ ...config, identity, relay_url: relayUrl, keys_dir: keyStore.keysDir });
      return write(
        streams,
        jsonOut,
        {
          identity,
          key_path: signingKeyPath(keyStore.keysDir, identity),
          encryption_key_path: encryptionKeyPath(keyStore.keysDir, identity),
          enrolled: true,
        },
        `enrolled ${identity} on this device\n`,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 2000));
  }
  throw new UsageError("timed out waiting for approval");
}

export async function keyClaim(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { ...JSON_FLAG, defaults: { relay: config.relay_url } });
  const identity = requirePositional(args, 0, "usage: poweur key claim <identity> <rendezvous-id> --ephemeral-key <b64url>");
  const rendezvousId = normalizeRendezvousId(requirePositional(args, 1, "rendezvous id is required"));
  const ephemeral = flagString(args, "ephemeral-key");
  if (!ephemeral) {
    throw new UsageError("--ephemeral-key is required (printed by `poweur key enroll`)");
  }
  const relayUrl = flagString(args, "relay");
  if (!relayUrl) throw new UsageError("--relay is required");

  const privateKey = decodeEphemeralKey(ephemeral);
  const seed = await enrollApi(relayUrl).claim(identity, {
    rendezvousId,
    ephemeralPrivateKey: privateKey,
  });
  if (!seed) {
    throw new UsageError("not approved yet — run `poweur key approve` on a device that has this identity");
  }
  const keyStore = new FileKeyStore(config.keys_dir);
  await keyStore.save(identityKeysFromSeed(identity, seed));
  saveConfig({ ...config, identity, relay_url: relayUrl, keys_dir: keyStore.keysDir });
  return write(
    streams,
    flagBool(args, "json"),
    {
      identity,
      key_path: signingKeyPath(keyStore.keysDir, identity),
      encryption_key_path: encryptionKeyPath(keyStore.keysDir, identity),
      enrolled: true,
    },
    `enrolled ${identity} on this device\n`,
  );
}

export async function keyApprove(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, {
    bool: ["json"],
    defaults: { relay: config.relay_url, "use-identity": config.identity },
  });
  const rendezvousId = normalizeRendezvousId(
    requirePositional(args, 0, "usage: poweur key approve <rendezvous-id> [--sas 123456]"),
  );
  const identity = flagString(args, "use-identity");
  const relayUrl = flagString(args, "relay");
  if (!identity || !relayUrl) throw new UsageError("identity and relay url required");
  const seedFlag = flagString(args, "seed");
  if (!seedFlag) {
    throw new UsageError("--seed is required: the seed lives only on your devices, never on the relay");
  }
  const seed = parseSeedOrMnemonic(seedFlag);
  const { signer } = signerFor(identityKeysFromSeed(identity, seed));
  const api = enrollApi(relayUrl);
  const pending = await api.pending(signer, identity, rendezvousId);
  const expectSas = flagString(args, "sas");
  if (expectSas && expectSas !== pending.sas) {
    throw new UsageError(
      `code mismatch: this rendezvous shows ${pending.sas}, you expected ${expectSas}\n` +
        "Do not approve — another device may be trying to enrol.",
    );
  }
  if (!expectSas && !flagBool(args, "json")) {
    streams.stderr(`confirm this matches the new device's screen: ${pending.sas}\n`);
  }
  await api.approve(signer, identity, pending, seed);
  return write(
    streams,
    flagBool(args, "json"),
    { identity, rendezvous_id: pending.rendezvous_id, sas: pending.sas, approved: true },
    `approved device ${pending.rendezvous_id} for ${identity}\n`,
  );
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
      throw new UsageError(
        "usage: poweur key <rotate|enroll|approve|claim> …",
      );
  }
}
