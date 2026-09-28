import { readFile } from "node:fs/promises";
import { FileChunkCache } from "../../node/drive-cache.js";
import { DriveClient } from "../../drive/client.js";
import { DriveFiles, DriveLog, fileKeys } from "../../drive/index.js";
import { normalizeName } from "../../drive/names.js";
import type { ShareRole } from "../../drive/share.js";
import { fromBase64 } from "../../encoding.js";
import { resolveIdentity } from "../../resolve.js";
import { nodeResolveOptions, openClient } from "../../node/session-factory.js";
import { flagBool, flagNumber, flagString, parseArgs, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";

const usage = "usage: poweur drive history|tail <path> [--from=1]; append <path> <file>; trim <log> <snapshot>; watch; share add <path> <member>|rm <id>|ls; link create <path>|rm <id>; transfer <path> --to <drive> [--into <path>|--to-node <id>] [--json]";

export async function driveOpsCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0]!;
  const args = parseArgs(argv.slice(1), { bool: ["json"] });
  const json = flagBool(args, "json");
  const { client, keys } = await openClient({ identity: flagString(args, "use-identity") });
  if (!keys.encryptionPrivateKey && sub !== "watch") throw new Error("identity has no encryption key");
  const drive = new DriveClient(client.relay, client.signer, client.signer.identity, new FileChunkCache());
  const files = keys.encryptionPrivateKey ? new DriveFiles(drive, fileKeys(keys.signingPrivateKey, keys.encryptionPrivateKey)) : undefined;
  const role = (flagString(args, "role") ?? "read") as ShareRole;
  const expires = flagString(args, "expires") ?? "";
  if (sub === "watch") {
    if (args.positional.length) throw new UsageError(usage);
    await drive.subscribe(event => { write(streams, json, event, `${event.type} ${event.timestamp}\n`); });
    return 0;
  }
  if (!files) throw new Error("identity has no encryption key");
  if (sub === "share" || sub === "link") {
    const action = args.positional[0];
    if (action === "ls" && sub === "share") return write(streams, json, await drive.shares(), "");
    if (action === "rm") {
      const id = args.positional[1];
      if (!id) throw new UsageError(usage);
      await drive.unshare(id);
      return write(streams, json, { removed: id }, `${id}\n`);
    }
    if (action === "add" && sub === "share") {
      const path = args.positional[1], member = args.positional[2];
      if (!path || !member) throw new UsageError(usage);
      const resolved = await resolveIdentity(member, nodeResolveOptions());
      const published = resolved.document.encryption_public_key;
      if (!published) throw new Error(`no encryption key for ${member}`);
      const memberKey = fromBase64(published.replace(/^x25519:/, ""));
      if (memberKey.length !== 32) throw new Error(`no encryption key for ${member}`);
      const share = await files.shareWith(await files.resolve(path), member, memberKey, role, expires);
      return write(streams, json, { id: share.id, node: share.node, member: share.member, role: share.role }, `${share.id}\n`);
    }
    if (action === "create" && sub === "link") {
      const path = args.positional[1];
      if (!path) throw new UsageError(usage);
      const { share, fragment } = await files.link(await files.resolve(path), role, expires, flagString(args, "password") ?? "");
      const secret = Buffer.from(fragment).toString("base64url");
      return write(streams, json, { id: share.id, link: share.link, role: share.role, fragment: secret }, `${share.link}#${secret}\n`);
    }
    throw new UsageError(usage);
  }
  if (sub === "history") {
    const file = await files.resolve(need(args.positional[0]));
    const versions = await drive.history(file.manifest.node);
    return write(streams, json, { node: file.manifest.node, ...versions }, `${versions.versions.join("\n")}\n`);
  }
  if (sub === "append") {
    const [path, local] = args.positional;
    if (!path || !local) throw new UsageError(usage);
    const { file, created } = await ensureAppend(files, path);
    const position = await files.append(file, new Uint8Array(await readFile(local)));
    return write(streams, json, { node: file.manifest.node, position, created }, `${file.manifest.node} ${position}\n`);
  }
  if (sub === "tail") {
    const file = await files.resolve(need(args.positional[0]));
    const records = await files.tail(file, flagNumber(args, "from", 0));
    const rows = records.map(record => ({ position: record.position, author: record.author, sequence: record.sequence, text: new TextDecoder().decode(record.plain) }));
    return write(streams, json, rows, `${rows.length}\n`);
  }
  if (sub === "trim") {
    const [logPath, snapPath] = args.positional;
    if (!logPath || !snapPath) throw new UsageError(usage);
    const log = await DriveLog.open(files, await files.resolve(logPath), (state, entry) => {
      const rows = Array.isArray(state) ? state as string[] : [];
      rows.push(new TextDecoder().decode(entry.plaintext));
      return rows;
    }, []);
    const { folder, name } = await parentOf(files, snapPath);
    const snap = await log.snapshot(folder, name);
    return write(streams, json, { node: snap.manifest.node, version: snap.manifest.version, through: log.position }, `${snap.manifest.version}\n`);
  }
  if (sub === "transfer") {
    const path = need(args.positional[0]);
    const to = flagString(args, "to");
    const toNode = flagString(args, "to-node");
    if (!to) throw new UsageError(usage);
    const file = await files.resolve(path);
    if (toNode) {
      await drive.commit({ transfer: { node: file.manifest.node, to, to_node: toNode } });
      return write(streams, json, { node: file.manifest.node, to, to_node: toNode }, `${toNode}\n`);
    }
    throw new UsageError("destination keys for a copy live with the caller; pass --to-node to retire a recreated subtree");
  }
  throw new UsageError(usage);
}
function need(value: string | undefined): string {
  if (!value) throw new UsageError(usage);
  return value;
}
async function parentOf(files: DriveFiles, path: string) {
  const remote = path.replace(/^\//, ""), at = remote.lastIndexOf("/");
  return { folder: await files.resolve(at < 0 ? "" : remote.slice(0, at)), name: normalizeName(remote.slice(at + 1)) };
}
async function ensureAppend(files: DriveFiles, path: string) {
  try { return { file: await files.resolve(path), created: false }; }
  catch {
    const { folder, name } = await parentOf(files, path);
    return { file: await files.create(folder, name, "file", new Uint8Array(), "append"), created: true };
  }
}
