import { readFile } from "node:fs/promises";
import { DriveFiles, DriveLog } from "../../drive/index.js";
import { normalizeName } from "../../drive/names.js";
import type { ShareRole } from "../../drive/share.js";
import { fromBase64 } from "../../encoding.js";
import { resolveIdentity } from "../../resolve.js";
import { nodeResolveOptions } from "../../node/session-factory.js";
import { openDrive } from "./drive-open.js";
import { offerShare, sendShareMessage } from "./drive-offers.js";
import { flagBool, flagNumber, flagString, parseArgs, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";

const usage = "usage: poweur drive history|tail <path> [--from=1]; append <path> <file>; trim <log> <snapshot>; watch; share add <path> <member>|rm <id>|ls; link create <path>|rm <id>; transfer <path> --to <drive> [--into </shared-node-id/path>|--to-node <id>]; any command takes --drive <identity> [--json]";

export async function driveOpsCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0]!;
  const args = parseArgs(argv.slice(1), { bool: ["json", "no-offer"] });
  const json = flagBool(args, "json");
  const { client, keys, drive, files } = await openDrive({ identity: flagString(args, "use-identity"), drive: flagString(args, "drive") });
  const notify = !flagBool(args, "no-offer");
  if (!keys.encryptionPrivateKey && sub !== "watch") throw new Error("identity has no encryption key");
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
      const revoked = (await drive.shares()).shares.find(s => s.id === id);
      await drive.unshare(id);
      if (revoked?.member && notify) {
        await sendShareMessage(client, revoked.member, "sys.share.revoked", id, { format: 2, drive: revoked.drive, share_id: id, revoked_at: new Date().toISOString().replace(/\.\d{3}Z$/, "Z") })
          .catch(error => streams.stderr(`revoked, but the member was not told: ${error}\n`));
      }
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
      const target = await files.resolve(path);
      const share = await files.shareWith(target, member, memberKey, role, expires);
      if (notify) await offerShare(client, drive.relay.relayUrl, target, share).catch(error => streams.stderr(`shared, but the offer was not sent: ${error}\n`));
      return write(streams, json, { id: share.id, node: share.node, member: share.member, role: share.role }, `${share.id}\n`);
    }
    if (action === "create" && sub === "link") {
      const path = args.positional[1];
      if (!path) throw new UsageError(usage);
      const { share, fragment } = await files.link(await files.resolve(path), role, expires, flagString(args, "password") ?? "");
      const secret = Buffer.from(fragment).toString("base64url");
      // Shared as https://<drive>/s/<link>#<secret>; the drive's host serves the viewer.
      const relayUrl = new URL(drive.relay.relayUrl);
      const url = `${relayUrl.protocol}//${share.drive}${relayUrl.port ? `:${relayUrl.port}` : ""}/s/${share.link}#${secret}`;
      return write(streams, json, { id: share.id, link: share.link, role: share.role, fragment: secret, url }, `${url}\n`);
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
    // Re-create the subtree on the destination as its owner or as a member
    // with a share there (--into /<shared-node-id>/...), then retire it here.
    const into = flagString(args, "into") ?? "";
    const destination = await openDrive({ identity: flagString(args, "use-identity"), drive: to });
    if (!destination.files) throw new Error("identity has no encryption key");
    const copied = await files.transfer(file, destination.files, await destination.files.resolve(into));
    return write(streams, json, { node: file.manifest.node, to, to_node: copied.manifest.node, name: copied.name }, `${copied.manifest.node}\n`);
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
