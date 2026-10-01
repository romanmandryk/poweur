/**
 * `poweur` — the TypeScript CLI, command-for-command with the Go CLI in
 * `apps/cli`, and reading the same `~/.poweur` tree, so the two are
 * interchangeable against one identity.
 *
 * Run it without installing:  npx @poweur/client inbox
 */

import { driveCommand } from "./commands/drive.js";
import { historyCommand } from "./commands/history.js";
import { UsageError } from "./args.js";
import { defaultStreams, fail, type Streams } from "./output.js";
import { identityCommand } from "./commands/identity.js";
import { keyCommand } from "./commands/enroll.js";
import {
  anon,
  inbox,
  listen,
  messagesStatus,
  relayCommand,
  send,
  sessionCommand,
} from "./commands/messaging.js";
import {
  analyticsCommand,
  authCommand,
  contactsCommand,
  policyCommand,
  requestsCommand,
} from "./commands/social.js";

export const HELP = `Usage:
  poweur identity create <name> [--dns-provider=cloudflare|hetzner] [--dns-token=...] [--parent-domain=...] [--relay=...] [--hosted] [--seed=<b64url|mnemonic>] [--json]
  poweur identity show [--use-identity=...] [--json]
  poweur identity dns <identity> [--json]
  poweur identity use <identity> [--json]
  poweur identity list [--json]
  poweur identity lookup <identity> [--json]
  poweur identity export [--use-identity=...] [--out=<file.tar.gz>]
  poweur key rotate [--use-identity=...] [--grace=168h] [--json]
  poweur key enroll <identity> [--relay=...] [--label=...] [--wait]
  poweur key approve <rendezvous-id> [--use-identity=...] [--seed=<b64url|mnemonic>] [--sas=<digits>] [--json]
  poweur key claim <identity> <rendezvous-id> --ephemeral-key <b64url> [--relay=...] [--json]
  poweur send <to> <message> [--sign-with=session|identity] [--type=...] [--via-home-relay] [--accept-new-key] [--use-identity=...] [--json]
  poweur send <to> <message> --anon      (unsigned; recipient must allow anonymous senders)
  poweur inbox [--use-identity=...] [--json [--decrypt]]      (--decrypt: add each message's plaintext as "body")
  poweur history [<peer>] [--limit=N] [--before=N] [--thread=...] [--keep-unread] [--use-identity=...] [--json]
  poweur listen [--once] [--use-identity=...] [--json [--decrypt]]
  poweur messages status [--id=<message-id>] [--use-identity=...] [--json]
  poweur anon [--use-identity=...] [--json]      (read your anonymous queue)
  poweur session <status|refresh|revoke> [--use-identity=...] [--json]
  poweur relay <status|set <url>> [--json]
  poweur contacts <ls|add|request|accept|block|rm> [<identity>] [--petname=...] [--use-identity=...] [--json]
  poweur requests [--use-identity=...] [--json]
  poweur policy <show|set <open|contacts_only|contacts_and_requests>> [--anon-allow] [--anon-challenge=none|pow] [--anon-bits=N] [--json]
  poweur auth <inspect|sign> <request-file-or-url> [--use-identity=...] [--json]
  poweur analytics <show|on|off> [--use-identity=...] [--json]
  poweur drive <info|node|ls|changes|records|history|append|tail|trim|watch|share|link|transfer> [--json]
  poweur version
`;

/** Dispatch one command line. Returns the process exit code. */
export async function run(argv: string[], streams: Streams = defaultStreams()): Promise<number> {
  if (argv.length === 0) {
    streams.stdout(HELP);
    return 0;
  }
  const [command, ...rest] = argv;

  try {
    switch (command) {
      case "drive": return await driveCommand(rest, streams);
      case "identity": return await identityCommand(rest, streams);
      case "key": return await keyCommand(rest, streams);
      case "send": return await send(rest, streams);
      case "inbox": return await inbox(rest, streams);
      case "history": return await historyCommand(rest, streams);
      case "listen": return await listen(rest, streams);
      case "messages":
        if (rest[0] !== "status") throw new UsageError("unknown messages subcommand (want status)");
        return await messagesStatus(rest.slice(1), streams);
      case "anon": return await anon(rest, streams);
      case "session": return await sessionCommand(rest, streams);
      case "relay": return await relayCommand(rest, streams);
      case "contacts": return await contactsCommand(rest, streams);
      case "requests": return await requestsCommand(rest, streams);
      case "policy": return await policyCommand(rest, streams);
      case "analytics": return await analyticsCommand(rest, streams);
      case "auth": return await authCommand(rest, streams);
      case "version":
      case "--version":
      case "-v": {
        const { SDK_VERSION, SDK_BUILD_TIME } = await import("../index.js");
        streams.stdout(`poweur (@poweur/client) ${SDK_VERSION}\n`);
        if (SDK_BUILD_TIME) streams.stdout(`built ${SDK_BUILD_TIME}\n`);
        return 0;
      }
      case "help":
      case "--help":
      case "-h":
        streams.stdout(HELP);
        return 0;
      default:
        streams.stderr("unknown command\n");
        streams.stderr(HELP);
        return 1;
    }
  } catch (error) {
    if (error instanceof UsageError) {
      streams.stderr(`${error.message}\n`);
      return 1;
    }
    return fail(streams, error);
  }
}
