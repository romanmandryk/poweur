import { driveFilesCommand } from "./drive-files.js";
import { driveOpsCommand } from "./drive-ops.js";
import { DriveClient } from "../../drive/client.js";
import { openClient } from "../../node/session-factory.js";
import { flagBool, flagNumber, flagString, parseArgs, requirePositional, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";

export async function driveCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  if (["mkdir", "put", "get", "mv", "rm", "list"].includes(sub ?? "")) return driveFilesCommand(argv, streams);
  if (["history", "append", "tail", "trim", "watch", "share", "link", "transfer"].includes(sub ?? "")) return driveOpsCommand(argv, streams);
  if (!["info", "node", "ls", "changes", "records"].includes(sub ?? "")) {
    throw new UsageError("usage: poweur drive <info|node|ls|changes|records|history|append|tail|trim|watch|share|link|transfer> [--json]");
  }
  const args = parseArgs(argv.slice(1), { bool: ["json"] });
  if (sub === "ls" && args.positional.length === 1 && !/^[0-9a-f]{32}$/.test(args.positional[0]!)) return driveFilesCommand(["list", ...argv.slice(1)], streams);
  const needsNode = ["node", "ls", "records"].includes(sub!);
  if (args.positional.length !== (needsNode ? 1 : 0)) throw new UsageError("unexpected drive arguments");
  const node = needsNode ? requirePositional(args, 0, "node ID required") : "";
  const limit = flagNumber(args, "limit", 100);
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 1000) throw new UsageError("limit must be between 1 and 1000");
  const cursor = flagString(args, "cursor") ?? "";
  const { client } = await openClient({ identity: flagString(args, "use-identity") });
  const drive = new DriveClient(client.relay, client.signer, flagString(args, "drive") || client.signer.identity);
  let result: unknown;
  switch (sub) {
    case "info": result = await drive.info(); break;
    case "node": result = await drive.node(node); break;
    case "ls": result = await drive.children(node, cursor, limit); break;
    case "changes": result = await drive.changes(cursor || "0", limit); break;
    case "records": {
      const from = Number(cursor || "0");
      if (!Number.isSafeInteger(from) || from < 0) throw new UsageError("invalid record cursor");
      result = await drive.records(node, from, limit); break;
    }
  }
  return write(streams, flagBool(args, "json"), result, JSON.stringify(result, null, 2));
}
