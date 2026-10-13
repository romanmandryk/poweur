import { flagBool, flagNumber, flagString, parseArgs, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";
import { openClient } from "../../node/session-factory.js";
import { historyPeer, recordThreadKey } from "../../history.js";

export async function historyCommand(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: ["json", "keep-unread", "by-subject"] });
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
  if (flagBool(args, "by-subject")) {
    // One row per (conversation, thread), newest first, labelled by subject.
    const rows = new Map<string, { peer: string; key: string; subject: string; last: string; count: number }>();
    for (const record of records) {
      const peer = historyPeer(record, client.signer.identity);
      const key = recordThreadKey(record);
      const id = `${peer}\u0000${key}`;
      const row = rows.get(id) ?? { peer, key, subject: "", last: record.timestamp, count: 0 };
      row.count += 1;
      row.last = record.timestamp;
      if (record.subject) row.subject = record.subject;
      rows.set(id, row);
    }
    const list = [...rows.values()].sort((a, b) => (a.last < b.last ? 1 : -1));
    if (flagBool(args, "json")) return write(streams, true, { identity: client.signer.identity, conversations: list }, "");
    for (const row of list) {
      const label = row.subject || (row.key && !row.key.startsWith("subj:") ? `#${row.key}` : "(no subject)");
      streams.stdout(`[${row.last}] ${row.peer}: ${label} (${row.count})\n`);
    }
    return 0;
  }
  if (flagBool(args, "json")) return write(streams, true, { identity: client.signer.identity, messages: records }, "");
  if (!records.length) {
    streams.stdout("no message history\n");
    return 0;
  }
  for (const record of records) {
    const who = record.queue === "sent" ? `→ ${record.recipient}` : record.queue === "anonymous" || !record.sender ? "ANONYMOUS" : record.sender;
    streams.stdout(`[${record.timestamp}] ${who}: ${record.subject ? `[${record.subject}] ` : ""}${record.body}\n`);
  }
  if (!flagBool(args, "keep-unread") && peer) await store.markConversationRead(peer, records);
  return 0;
}
