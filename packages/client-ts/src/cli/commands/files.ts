/** `poweur dav …`, `poweur sync …`, `poweur share …`. */

import { mkdirSync } from "node:fs";
import { resolve as resolvePath } from "node:path";

import { PoweurError } from "../../errors.js";
import { loadState, saveState, Ignore, SyncEngine, type SyncReport } from "../../node/syncengine.js";
import { openClient } from "../../node/session-factory.js";
import { SyncClient } from "../../sync.js";
import {
  flagBool,
  flagList,
  flagString,
  parseArgs,
  repeated,
  requirePositional,
  UsageError,
} from "../args.js";
import { write, type Streams } from "../output.js";

const COMMON_BOOL = ["json"];

const APP_PASSWORDS_PATH = "poweur-sys/relay/app-passwords.json";

interface AppPassword {
  name: string;
  hash: string;
  scope: string;
  created_at: string;
}

interface AppPasswordsFile {
  version?: number;
  passwords: AppPassword[];
}

export async function davCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  switch (sub) {
    case "token": return davToken(argv.slice(1), streams);
    case "mount": return davMount(argv.slice(1), streams);
    case "password": return davPassword(argv.slice(1), streams);
    default:
      throw new UsageError("usage: poweur dav <token|mount|password>");
  }
}

async function davToken(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
    ...(flagString(args, "relay") ? { relayUrl: flagString(args, "relay") } : {}),
  });
  const token = await client.davToken({
    ...(flagString(args, "audience") ? { audience: flagString(args, "audience") } : {}),
    ...(flagString(args, "scope") ? { scope: flagString(args, "scope") } : {}),
  });
  if (flagBool(args, "json")) return write(streams, true, token, "");
  streams.stdout(
    `token:      ${token.token}\naudience:   ${token.audience}\nscope:      ${token.scope}\n` +
      `expires_at: ${token.expires_at}\n\ncurl example:\n` +
      `  curl -H 'Authorization: Bearer ${token.token}' -X PROPFIND -H 'Depth: 1' ` +
      `${client.relay.relayUrl}/dav/${token.audience}/\n`,
  );
  return 0;
}

/** Print ready-to-paste mount commands for the common DAV clients. */
async function davMount(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const { loadConfig } = await import("../../node/config.js");
  const config = loadConfig();
  const identity = flagString(args, "use-identity") || config.identity;
  if (!identity || !config.relay_url) throw new UsageError("identity and relay url required");
  const url = `${config.relay_url.replace(/\/+$/, "")}/dav/${identity}/`;

  streams.stdout(
    `WebDAV URL: ${url}\nUsername:   ${identity}\n` +
      "Password:   an app password — create one with `poweur dav password add --name mymac`\n\n" +
      "macOS (Finder):\n" +
      `  open "${url}"   # or Finder → Go → Connect to Server…\n` +
      "macOS (terminal):\n" +
      `  mkdir -p ~/poweur && mount_webdav -i ${url} ~/poweur\n` +
      "Linux (davfs2):\n" +
      `  sudo mount -t davfs ${url} /mnt/poweur\n` +
      "Windows:\n" +
      `  net use P: ${url} /user:${identity}\n` +
      "rclone:\n" +
      `  rclone lsd :webdav: --webdav-url ${url} --webdav-user ${identity} --webdav-pass <app-password>\n`,
  );
  return 0;
}

/**
 * App passwords for legacy Basic-auth DAV clients (Finder and friends).
 * Only the hash is stored; the password is shown once and never again.
 */
async function davPassword(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const args = parseArgs(argv.slice(1), { bool: COMMON_BOOL, defaults: { scope: "dav:full" } });
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const dav = await client.dav();
  const raw = await dav.readOptional(APP_PASSWORDS_PATH);
  const file: AppPasswordsFile = raw ? (JSON.parse(raw) as AppPasswordsFile) : { passwords: [] };
  file.passwords ??= [];

  if (sub === "list") {
    if (flagBool(args, "json")) return write(streams, true, file, "");
    if (file.passwords.length === 0) {
      streams.stdout("no app passwords\n");
      return 0;
    }
    for (const password of file.passwords) {
      streams.stdout(`${password.name}\tscope=${password.scope}\tcreated=${password.created_at}\n`);
    }
    return 0;
  }

  const name = flagString(args, "name");
  if (!name) throw new UsageError("--name is required");

  if (sub === "add") {
    if (file.passwords.some((p) => p.name === name)) {
      throw new UsageError(`app password "${name}" already exists (remove it first)`);
    }
    const { generateAppPassword, hashAppPassword } = await import("../../apppass.js");
    const password = generateAppPassword();
    file.passwords.push({
      name,
      hash: await hashAppPassword(password),
      scope: flagString(args, "scope", "dav:full"),
      created_at: new Date().toISOString().replace(/\.\d{3}Z$/, "Z"),
    });
    await dav.writeJson(APP_PASSWORDS_PATH, file);
    return write(
      streams,
      flagBool(args, "json"),
      { name, password, scope: flagString(args, "scope", "dav:full"), username: client.identityName },
      `app password "${name}" created.\n  username: ${client.identityName}\n  password: ${password}\n` +
        "Store it now — it is shown only once.\n",
    );
  }

  if (sub === "remove") {
    const kept = file.passwords.filter((p) => p.name !== name);
    if (kept.length === file.passwords.length) {
      throw new UsageError(`app password "${name}" not found`);
    }
    file.passwords = kept;
    await dav.writeJson(APP_PASSWORDS_PATH, file);
    streams.stdout(`app password "${name}" removed\n`);
    return 0;
  }

  throw new UsageError("unknown dav password subcommand (want add, list, remove)");
}

export async function syncCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  if (!["pull", "push", "run", "status"].includes(sub ?? "")) {
    throw new UsageError("usage: poweur sync <pull|push|run|status> <local-dir> [--path <prefix> ...]");
  }
  const args = parseArgs(argv.slice(1), { bool: COMMON_BOOL, repeatable: ["path"] });
  const dir = requirePositional(args, 0, "sync needs exactly one local directory");
  const root = resolvePath(dir);
  mkdirSync(root, { recursive: true, mode: 0o700 });

  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
    ...(flagString(args, "relay") ? { relayUrl: flagString(args, "relay") } : {}),
  });
  const owner = flagString(args, "audience") || client.identityName;

  const state = loadState(root);
  if (state.identity && state.identity.toLowerCase() !== owner.toLowerCase()) {
    throw new UsageError(`directory is already syncing with ${state.identity} (not ${owner})`);
  }
  state.identity = owner;

  const dav = await client.dav(owner === client.identityName ? {} : { audience: owner });
  const engine = new SyncEngine({
    root,
    dav,
    sync: new SyncClient(client.relay, dav.identity, dav.token),
    state,
    ignore: Ignore.load(root),
    roots: repeated(args, "path").map((p) => p.replace(/^\/+|\/+$/g, "")).filter(Boolean),
    log: (message) => streams.stderr(`${message}\n`),
  });

  if (sub === "status") {
    const status = await engine.status();
    for (const path of status.localNew) streams.stdout(`local new: ${path}\n`);
    for (const path of status.localModified) streams.stdout(`local modified: ${path}\n`);
    for (const path of status.localDeleted) streams.stdout(`local deleted: ${path}\n`);
    if (status.needsResync) {
      streams.stdout("remote: full resync required (first sync or journal gap)\n");
    } else {
      streams.stdout(`remote: ${status.remotePending} pending change(s)\n`);
    }
    saveState(root, state);
    return 0;
  }

  if (sub === "pull") {
    printReport(streams, await engine.pull());
    return 0;
  }
  if (sub === "push") {
    printReport(streams, await engine.push());
    return 0;
  }

  const { pull, push } = await engine.run();
  printReport(streams, pull);
  printReport(streams, push);
  if (
    [pull, push].every((report) => Object.values(report).every((list) => list.length === 0))
  ) {
    streams.stdout("already in sync\n");
  }
  return 0;
}

function printReport(streams: Streams, report: SyncReport): void {
  for (const path of report.mkdirLocal) streams.stdout(`mkdir (local): ${path}\n`);
  for (const path of report.downloaded) streams.stdout(`downloaded: ${path}\n`);
  for (const path of report.deletedLocal) streams.stdout(`deleted (local): ${path}\n`);
  for (const path of report.mkdirRemote) streams.stdout(`mkdir (remote): ${path}\n`);
  for (const path of report.uploaded) streams.stdout(`uploaded: ${path}\n`);
  for (const path of report.deletedRemote) streams.stdout(`deleted (remote): ${path}\n`);
  for (const path of report.conflicts) {
    streams.stdout(`CONFLICT — local version saved as: ${path}\n`);
  }
}

export async function shareCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  switch (sub) {
    case "add": return shareAdd(argv.slice(1), streams);
    case "ls": return shareLs(argv.slice(1), streams);
    case "revoke": return shareRevoke(argv.slice(1), streams);
    case "group": return shareGroup(argv.slice(1), streams);
    default:
      throw new UsageError("usage: poweur share <add|ls|revoke|group>");
  }
}

function describeAudience(audience: Array<{ id?: string; group?: string }>): string {
  return audience.map((entry) => (entry.id ? entry.id : `group:${entry.group}`)).join(", ");
}

async function shareAdd(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, {
    bool: COMMON_BOOL,
    repeatable: ["with", "with-group"],
    defaults: { perm: "read" },
  });
  const path = requirePositional(
    args,
    0,
    "usage: poweur share add <path> --with <id> [--with-group <name>] [--perm read|rw] [--expires ...]",
  );
  const perm = flagString(args, "perm", "read");
  if (!["read", "rw", "read-write", "write"].includes(perm)) {
    throw new UsageError("--perm must be read or rw");
  }
  const expires = flagString(args, "expires");
  if (expires && Number.isNaN(Date.parse(expires))) {
    throw new UsageError(`invalid --expires: ${expires}`);
  }

  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const shares = await client.shares();
  const grant = await shares.add(client.signer, path, {
    with: repeated(args, "with"),
    withGroups: repeated(args, "with-group"),
    permissions: perm === "read" ? "read" : "rw",
    ...(expires ? { expiresAt: expires } : {}),
  });
  return write(
    streams,
    flagBool(args, "json"),
    grant,
    `share ${grant.share_id} created\n  path: /${grant.path}\n` +
      `  audience: ${describeAudience(grant.audience)}\n  permissions: ${grant.permissions.join(",")}\n`,
  );
}

async function shareLs(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const grants = await (await client.shares()).list();
  if (flagBool(args, "json")) return write(streams, true, grants, "");
  if (grants.length === 0) {
    streams.stdout("no shares\n");
    return 0;
  }
  for (const grant of grants) {
    streams.stdout(
      `${grant.share_id}\t/${grant.path}\t${describeAudience(grant.audience)}\t` +
        `perm=${grant.permissions.join(",")}\texpires=${grant.expires_at || "never"}\n`,
    );
  }
  return 0;
}

async function shareRevoke(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const shareId = requirePositional(args, 0, "usage: poweur share revoke <share-id>");
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const removed = await (await client.shares()).revoke(shareId);
  if (!removed) throw new PoweurError("not_found", `share ${shareId} not found`);
  streams.stdout(`share ${shareId} revoked\n`);
  return 0;
}

async function shareGroup(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const args = parseArgs(argv.slice(1), { bool: COMMON_BOOL });
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const shares = await client.shares();

  if (sub === "set") {
    const name = requirePositional(
      args,
      0,
      "usage: poweur share group set <name> --members bob.example.org,carol.poweur.net",
    );
    const members = flagList(args, "members");
    const group = await shares.setGroup(client.signer, name, members);
    return write(
      streams,
      flagBool(args, "json"),
      group,
      `group "${name}" set (${members.length} member(s))\n`,
    );
  }
  if (sub === "ls") {
    const groups = await shares.listGroups();
    if (flagBool(args, "json")) return write(streams, true, groups, "");
    if (groups.length === 0) {
      streams.stdout("no groups\n");
      return 0;
    }
    for (const group of groups) {
      streams.stdout(`${group.group}\t${group.members.join(", ")}\n`);
    }
    return 0;
  }
  if (sub === "remove") {
    const name = requirePositional(args, 0, "usage: poweur share group remove <name>");
    const removed = await shares.removeGroup(name);
    if (!removed) throw new PoweurError("not_found", `group "${name}" not found`);
    streams.stdout(`group "${name}" removed\n`);
    return 0;
  }
  throw new UsageError("unknown share group subcommand (want set, ls, remove)");
}
