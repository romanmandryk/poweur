/** `poweur send`, `inbox`, `messages status`, `anon`, `session …`. */

import { rfc3339 } from "../../encoding.js";
import { RelayClient } from "../../http.js";
import { streamForever } from "../../events.js";
import { sendAnonymous } from "../../messages.js";
import type { PoweurClient } from "../../client.js";
import type { Ack, InboxMessage } from "../../types.js";
import {
  appendJournal,
  journalStatuses,
  STATE_DELIVERED_CLIENT,
  STATE_DELIVERED_RECIPIENT_RELAY,
  STATE_FAILED,
  STATE_QUEUED,
  tickGlyph,
} from "../../node/journal.js";
import { loadConfig } from "../../node/config.js";
import { nodeResolveOptions, openClient } from "../../node/session-factory.js";
import { flagBool, flagString, parseArgs, requirePositional, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";

const COMMON_BOOL = ["json"];

/** `--decrypt` hands a script the plaintext, so it never needs the message key. */
function requireJsonForDecrypt(args: ReturnType<typeof parseArgs>): boolean {
  const decrypt = flagBool(args, "decrypt");
  if (decrypt && !flagBool(args, "json")) {
    throw new UsageError("--decrypt only applies with --json (the default output is already decrypted)");
  }
  return decrypt;
}

/**
 * The pickup behind `--json --decrypt`, shared by `inbox` and `listen` and
 * identical in shape to the Go CLI's: the relay's inbox response, with each
 * message gaining `decrypted` and `body`. A message that did not open keeps
 * its ciphertext in `payload` and gets the failure text as `body`.
 *
 * It is a full pickup, not a print: delivery receipts, read receipts (per the
 * inbox policy), the sender's journal, and history all happen as in a human
 * pickup. Anything human-readable goes to stderr; stdout stays one document.
 */
async function decryptedPickup(
  client: PoweurClient,
  pickup: { messages: InboxMessage[]; acks: Ack[] },
  streams: Streams,
): Promise<string> {
  for (const ack of pickup.acks) {
    if (ack.state === STATE_DELIVERED_CLIENT && ack.message_id) {
      appendJournal({
        message_id: ack.message_id,
        sender: client.identityName,
        recipient: ack.sender,
        timestamp: rfc3339(),
        state: STATE_DELIVERED_CLIENT,
        detail: `ack id=${ack.id}`,
      });
    }
  }
  const opened = pickup.messages.filter((m) => m.plaintext !== null && Boolean(m.encryption?.alg));
  await client.ackDecrypted(opened);
  await client.sendReadReceipts(opened);
  const { lost } = await client.archiveInbound(opened);
  if (lost > 0) streams.stderr(`warning: ${lost} message(s) could not be archived to history\n`);

  const messages = pickup.messages.map((message) => {
    const { plaintext, decryptError, ...wire } = message;
    const decrypted = plaintext !== null && Boolean(message.encryption?.alg);
    let body: string;
    if (decrypted) body = plaintext as string;
    else if (!message.encryption?.alg) body = message.payload;
    else if (client.decryptor) body = `[decrypt failed: ${decryptError ?? "unknown"}]`;
    else body = "[encrypted: no local encryption key]";
    return { ...wire, decrypted, body };
  });
  return JSON.stringify({ messages, acks: pickup.acks });
}

export async function send(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, {
    bool: [...COMMON_BOOL, "via-home-relay", "accept-new-key", "anon"],
    defaults: { "sign-with": "session" },
  });
  const recipient = requirePositional(args, 0, "usage: poweur send <to> <message> [--sign-with=session|identity] [--via-home-relay] [--anon]");
  const plaintext = requirePositional(args, 1, "usage: poweur send <to> <message>");
  const jsonOut = flagBool(args, "json");

  // The anonymous path needs no identity at all — it is the one send that
  // works before you have one.
  if (flagBool(args, "anon")) {
    const config = loadConfig();
    const scheme = config.relay_url.startsWith("http://") ? "http" : "https";
    const result = await sendAnonymous(recipient, plaintext, {
      resolve: nodeResolveOptions(),
      scheme,
      onChallenge: ({ bits }) => {
        streams.stderr(
          `recipient requires proof-of-work (${bits} bits, ~2^${bits} hashes) — solving…\n`,
        );
      },
    });
    return write(
      streams,
      jsonOut,
      { id: result.id, status: result.status, anonymous: true, target_relay: result.targetRelay },
      `sent anonymous encrypted message to ${recipient} (id=${result.id})\n`,
    );
  }

  const signWith = flagString(args, "sign-with", "session").toLowerCase();
  if (signWith !== "session" && signWith !== "identity") {
    throw new UsageError(`invalid --sign-with value "${signWith}": must be "session" or "identity"`);
  }

  const opened = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const { client, config } = opened;

  // Key pinning (E07-T4): a pinned contact whose key changed without a signed
  // rotation is refused. This is the known-hosts moment — a silent send here
  // would be exactly the failure the pin exists to prevent.
  try {
    const contacts = await client.contacts();
    const pin = await contacts.checkPin(recipient);
    if (pin.status === "mismatch" && !flagBool(args, "accept-new-key")) {
      streams.stderr(
        `REFUSING TO SEND: ${recipient}'s current key does not match the pinned key and no rotation statement covers it.\n` +
          `  pinned:   ${pin.pinnedKey}\n  resolved: ${pin.resolvedKey}\n` +
          "This can mean a compromised relay or registrar impersonating your contact.\n" +
          "Verify out of band, then re-send with --accept-new-key to trust the new key.\n",
      );
      return 1;
    }
    if (pin.status === "rotated" && pin.resolvedKey) {
      streams.stderr(
        `note: ${recipient} rotated their key (old pin found in previous_keys); re-pinning\n`,
      );
      await contacts.repin(recipient, pin.resolvedKey);
    }
    if (pin.status === "mismatch" && pin.resolvedKey) {
      streams.stderr(
        `WARNING: re-pinning ${recipient} to a new key on your instruction (--accept-new-key)\n`,
      );
      await contacts.repin(recipient, pin.resolvedKey);
    }
  } catch {
    // Contacts unavailable (no relay tree yet, DAV down) → fail open. Pinning
    // is client-side defence in depth, not the security boundary.
  }

  const viaHomeRelay = flagBool(args, "via-home-relay") || config.via_home_relay;
  const messageId = { current: "" };
  try {
    const result = await client.send(recipient, plaintext, {
      signWith,
      viaHomeRelay,
      ...(flagString(args, "type") ? { type: flagString(args, "type") } : {}),
    });
    messageId.current = result.message.id;
    // Journal both transitions so `messages status` can render ticks later,
    // even after the relay has dropped its in-memory copy.
    appendJournal({
      message_id: result.message.id,
      sender: result.message.sender,
      recipient,
      timestamp: rfc3339(),
      state: STATE_QUEUED,
      ...(viaHomeRelay ? { via_home_relay: true } : {}),
    });
    appendJournal({
      message_id: result.message.id,
      sender: result.message.sender,
      recipient,
      timestamp: rfc3339(),
      state: STATE_DELIVERED_RECIPIENT_RELAY,
      ...(viaHomeRelay ? { via_home_relay: true } : {}),
    });
    return write(
      streams,
      jsonOut,
      {
        id: result.message.id,
        status: result.status,
        message: result.message,
        encrypted: true,
        sign_with: signWith,
        target_relay: result.targetRelay,
        via_home_relay: viaHomeRelay,
      },
      `sent encrypted message to ${recipient} (id=${result.message.id}${
        signWith === "identity" ? ", signed with identity key" : ""
      }, tick 1 ✓)\n`,
    );
  } catch (error) {
    if (messageId.current) {
      appendJournal({
        message_id: messageId.current,
        sender: client.identityName,
        recipient,
        timestamp: rfc3339(),
        state: STATE_FAILED,
        detail: error instanceof Error ? error.message : String(error),
        ...(viaHomeRelay ? { via_home_relay: true } : {}),
      });
    }
    throw error;
  }
}

/**
 * `poweur listen` — hold a push stream open and print what arrives
 * (EPIC-009 E09-T2).
 *
 * The stream is a cue, not a delivery: every notification triggers a cursor
 * read, which is where the messages actually come from. That is why a dropped
 * connection costs nothing — reconnecting picks up from the same cursor.
 */
export async function listen(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: [...COMMON_BOOL, "once", "decrypt"] });
  const decrypt = requireJsonForDecrypt(args);
  const once = flagBool(args, "once");
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const jsonOut = flagBool(args, "json");
  const controller = new AbortController();
  const stop = () => controller.abort();
  process.once("SIGINT", stop);
  process.once("SIGTERM", stop);

  let cursor = "";
  // Reports whether anything arrived, which is what --once exits on.
  const drain = async (): Promise<boolean> => {
    const { messages, acks, cursor: next } = await client.messages.inbox(
      client.signer, client.decryptor, { since: cursor },
    );
    if (decrypt) {
      streams.stdout((await decryptedPickup(client, { messages, acks }, streams)) + "\n");
    } else {
      for (const message of messages) {
        if (jsonOut) {
          streams.stdout(JSON.stringify(message) + "\n");
        } else {
          streams.stdout(`${message.sender}: ${message.plaintext ?? "<could not decrypt>"}\n`);
        }
      }
      for (const ack of acks) {
        if (!jsonOut) streams.stdout(`ack ${ack.message_id} ${ack.state}\n`);
      }
    }
    const delivered = messages.length > 0 || acks.length > 0;
    if (next && delivered) {
      // Only forget once the messages have actually been printed.
      await client.messages.consume(client.signer, { through: next, ackThrough: next });
    }
    if (next) cursor = "";
    return delivered;
  };
  // One pickup at a time. Opening the stream and every event it carries ask
  // for a drain, and two overlapping reads would both see — and act on — a
  // message that has not been consumed yet.
  let queue: Promise<void> = Promise.resolve();
  const pickup = () => {
    queue = queue.then(async () => {
      if (controller.signal.aborted) return;
      try {
        if ((await drain()) && once) stop();
      } catch (error) {
        streams.stderr(`pickup failed: ${String(error)}\n`);
      }
    });
  };

  streams.stderr(`listening as ${client.identityName} (ctrl-c to stop)\n`);
  await streamForever(client.relay, client.signer, {
    signal: controller.signal,
    onOpen: pickup,
    onEvent: (event) => {
      if (event.type === "ready") return; // the open handler already drained
      pickup();
    },
    onError: (error) => streams.stderr(`stream dropped, retrying: ${String(error)}\n`),
  });
  return 0;
}

export async function inbox(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: [...COMMON_BOOL, "decrypt"] });
  const decrypt = requireJsonForDecrypt(args);
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const { messages, acks } = await client.inbox();

  if (decrypt) {
    streams.stdout((await decryptedPickup(client, { messages, acks }, streams)) + "\n");
    return 0;
  }

  if (flagBool(args, "json")) {
    return write(streams, true, { messages, acks }, "");
  }

  // Acks first: an otherwise-empty inbox can still carry tick-2 receipts for
  // messages we sent earlier.
  for (const ack of acks) {
    if (ack.state === STATE_DELIVERED_CLIENT && ack.message_id) {
      appendJournal({
        message_id: ack.message_id,
        sender: client.identityName,
        recipient: ack.sender,
        timestamp: rfc3339(),
        state: STATE_DELIVERED_CLIENT,
        detail: `ack id=${ack.id}`,
      });
    }
    streams.stdout(
      `✓✓ [${ack.timestamp}] ${ack.state} delivered to ${ack.sender} (msg ${ack.message_id})\n`,
    );
  }

  if (messages.length === 0 && acks.length === 0) {
    streams.stdout("no messages\n");
    return 0;
  }

  for (const message of messages) {
    const decrypted = message.plaintext !== null && Boolean(message.encryption?.alg);
    const display =
      message.plaintext ??
      (client.decryptor
        ? `[decrypt failed: ${message.decryptError ?? "unknown"}]`
        : "[encrypted: no local encryption key]");
    streams.stdout(`${decrypted ? "🔒" : "  "} [${message.timestamp}] ${message.sender}: ${display}\n`);

    // Tick 2 is emitted only for messages we actually decrypted — that is the
    // proof it reached a client rather than merely a relay.
    if (decrypted && message.id && message.sender) {
      try {
        await client.messages.ack(client.signer, {
          id: message.id,
          sender: message.sender,
          ...(message.recipient ? { recipient: message.recipient } : {}),
        });
      } catch (error) {
        streams.stderr(
          `warning: failed to send delivery ack for ${message.id}: ${
            error instanceof Error ? error.message : String(error)
          }\n`,
        );
      }
    }
  }
  return 0;
}

export async function messagesStatus(argv: string[], streams: Streams): Promise<number> {
  const config = loadConfig();
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const identity = flagString(args, "use-identity") || config.identity;
  if (!identity) throw new UsageError("identity not configured");

  let statuses = journalStatuses(identity);
  const filter = flagString(args, "id");
  if (filter) statuses = statuses.filter((status) => status.message_id === filter);

  if (flagBool(args, "json")) return write(streams, true, statuses, "");
  if (statuses.length === 0) {
    streams.stdout("no pending messages\n");
    return 0;
  }
  for (const status of statuses) {
    streams.stdout(
      `${tickGlyph(status.state)}  ${status.message_id}  to=${status.recipient}  state=${status.state}\n`,
    );
  }
  return 0;
}

export async function anon(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const messages = await client.anon();
  if (flagBool(args, "json")) return write(streams, true, { messages }, "");
  if (messages.length === 0) {
    streams.stdout("no anonymous messages\n");
    return 0;
  }
  for (const message of messages) {
    const display = message.plaintext ?? "[encrypted: no local encryption key]";
    streams.stdout(`ANONYMOUS\t${message.timestamp}\t${display}\n`);
  }
  streams.stdout("\nnote: anonymous messages are unauthenticated — treat content accordingly\n");
  return 0;
}

export async function sessionCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const args = parseArgs(argv.slice(1), { bool: COMMON_BOOL });
  const jsonOut = flagBool(args, "json");
  const identityOverride = flagString(args, "use-identity");

  if (sub === "status") {
    const config = loadConfig();
    const identity = identityOverride || config.identity;
    if (!identity) throw new UsageError("identity not configured");
    const { client } = await openClient({ identity });
    const { session, valid } = await client.sessions.status(identity);
    if (!session?.sessionId) {
      streams.stdout(`no session for ${identity}\n`);
      return 0;
    }
    return write(
      streams,
      jsonOut,
      {
        identity: session.identity,
        session_id: session.sessionId,
        issued_at: session.issuedAt,
        expires_at: session.expiresAt,
        relay_url: session.relayUrl,
        valid,
      },
      `session ${session.sessionId} for ${session.identity} (valid=${valid}, expires ${session.expiresAt})\n`,
    );
  }

  const { client } = await openClient(identityOverride ? { identity: identityOverride } : {});
  if (sub === "refresh") {
    const session = await client.sessions.refresh(client.signer);
    return write(
      streams,
      jsonOut,
      { session_id: session.sessionId, expires_at: session.expiresAt },
      `session refreshed: ${session.sessionId} (expires ${session.expiresAt})\n`,
    );
  }
  if (sub === "revoke") {
    const { relayRevoked } = await client.sessions.revoke(client.signer);
    return write(
      streams,
      jsonOut,
      { identity: client.identityName, relay_revoked: relayRevoked },
      `session revoked for ${client.identityName}\n`,
    );
  }
  throw new UsageError("unknown session subcommand (want status, refresh, revoke)");
}

export async function relayCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const args = parseArgs(argv.slice(1), { bool: COMMON_BOOL });
  const config = loadConfig();

  if (sub === "status") {
    if (!config.relay_url) throw new UsageError("relay url not configured");
    const health = await new RelayClient(config.relay_url).request<{
      status: string;
      version: string;
      buildTime?: string;
      versionHash?: string;
      storage?: unknown;
    }>({ method: "GET", path: "/health" });
    const extra = health.buildTime ? `, ${health.buildTime}` : "";
    return write(
      streams,
      flagBool(args, "json"),
      {
        status: health.status,
        version: health.version,
        ...(health.buildTime ? { buildTime: health.buildTime } : {}),
        ...(health.versionHash ? { versionHash: health.versionHash } : {}),
        ...(health.storage ? { storage: health.storage } : {}),
      },
      `relay ${health.status} (version ${health.version}${extra})\n`,
    );
  }
  if (sub === "set") {
    const url = requirePositional(args, 0, "usage: poweur relay set <url>");
    const { saveConfig } = await import("../../node/config.js");
    saveConfig({ ...config, relay_url: url.replace(/\/+$/, "") });
    streams.stdout(`relay url set to ${url.replace(/\/+$/, "")}\n`);
    streams.stdout("note: self-hosted IDs should also update id.json relay field and DNS A/CNAME\n");
    return 0;
  }
  throw new UsageError("usage: poweur relay status|set <url>");
}
