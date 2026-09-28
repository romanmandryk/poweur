import { flagBool, flagNumber, flagString, parseArgs, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";
import { openClient } from "../../node/session-factory.js";

export async function historyCommand(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: ["json", "keep-unread"] });
  const limit = flagNumber(args, "limit", 0);
  const before = flagNumber(args, "before", 0);
  if (limit < 0 || before < 0) throw new UsageError("limit and before must be zero or positive");
  const peer = args.positional[0] ?? "";
  if (before > 0 && !peer) throw new UsageError("history --before needs a conversation");
  const { client } = await openClient({ identity: flagString(args, "use-identity") });
  const store = await client.history();
  let records = before > 0 ? await store.before(peer, before) : peer ? await store.tail(peer) : await store.load();
  const thread = flagString(args, "thread");
  if (thread) records = records.filter(record => record.thread_id === thread);
  if (limit > 0) records = records.slice(-limit);
  if (flagBool(args, "json")) return write(streams, true, { identity: client.signer.identity, messages: records }, "");
  if (!records.length) {
    streams.stdout("no message history\n");
    return 0;
  }
  for (const record of records) {
    const who = record.queue === "sent" ? `→ ${record.recipient}` : record.queue === "anonymous" || !record.sender ? "ANONYMOUS" : record.sender;
    streams.stdout(`[${record.timestamp}] ${who}: ${record.body}\n`);
  }
  if (!flagBool(args, "keep-unread") && peer) await store.markConversationRead(peer, records);
  return 0;
}
