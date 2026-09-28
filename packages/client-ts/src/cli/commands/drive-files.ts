import { FileChunkCache } from "../../node/drive-cache.js";
import { readFile } from "node:fs/promises";
import { open, rename, unlink } from "node:fs/promises";
import { dirname, join } from "node:path";
import { randomUUID } from "node:crypto";
import { DriveClient } from "../../drive/client.js";
import { DriveFiles, fileKeys } from "../../drive/files.js";
import { normalizeName } from "../../drive/names.js";
import { openClient } from "../../node/session-factory.js";
import { flagBool, flagString, parseArgs, UsageError } from "../args.js";
import { write, type Streams } from "../output.js";

export async function driveFilesCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0]!;
  const args = parseArgs(argv.slice(1), { bool: ["json"] });
  const count = ["put", "get", "mv"].includes(sub) ? 2 : 1;
  if (args.positional.length !== count) throw new UsageError("drive file command needs remote path (put/get/mv need two paths)");
  const [first, second] = args.positional as [string, string];
  const { client, keys } = await openClient({ identity: flagString(args, "use-identity") });
  if (!keys.encryptionPrivateKey) throw new Error("identity has no encryption key");
  const drive = new DriveClient(client.relay, client.signer, client.signer.identity, new FileChunkCache());
  const files = new DriveFiles(drive, fileKeys(keys.signingPrivateKey, keys.encryptionPrivateKey));
  const parent = async (path: string) => {
    const remote = path.replace(/^\//, ""), at = remote.lastIndexOf("/");
    const name = normalizeName(remote.slice(at + 1));
    return { folder: await files.resolve(at < 0 ? "" : remote.slice(0, at)), name };
  };
  let result: unknown;
  switch (sub) {
    case "list": {
      const children = await files.list(await files.resolve(first));
      result = children.map(file => ({ node: file.manifest.node, name: file.name, kind: file.manifest.kind, version: file.manifest.version })); break;
    }
    case "mkdir": {
      const { folder, name } = await parent(first);
      const file = await files.create(folder, name, "folder"); result = { node: file.manifest.node, name: file.name }; break;
    }
    case "put": {
      const bytes = new Uint8Array(await readFile(first));
      const { folder, name } = await parent(second);
      let file = (await files.list(folder)).find(entry => entry.name === name);
      if (file) await files.replace(file, bytes); else file = await files.create(folder, name, "file", bytes);
      result = { node: file.manifest.node, name: file.name, version: file.manifest.version }; break;
    }
    case "get": {
      const file = await files.resolve(first);
      const temporary = join(dirname(second), `.poweur-download-${randomUUID()}`);
      const output = await open(temporary, "wx", 0o600);
      try {
        for await (const chunk of files.read(file)) await output.writeFile(chunk);
        await output.sync(); await output.close(); await rename(temporary, second);
      } finally { await output.close(); await unlink(temporary).catch(error => { if (error.code !== "ENOENT") throw error; }); }
      result = { node: file.manifest.node, path: second }; break;
    }
    case "mv": {
      const file = await files.resolve(first), { folder, name } = await parent(second);
      await files.move(file, folder, name); result = { node: file.manifest.node, name: file.name }; break;
    }
    case "rm": {
      const file = await files.resolve(first); await files.remove(file); result = { removed: file.manifest.node }; break;
    }
  }
  return write(streams, flagBool(args, "json"), result, JSON.stringify(result, null, 2) + "\n");
}
