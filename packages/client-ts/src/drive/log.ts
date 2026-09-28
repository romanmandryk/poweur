/** Append files as ordered event logs: snapshot, tail, subscribe, append, trim. */
import type { DriveFiles, OpenFile } from "./files.js";
import { normalizeName } from "./names.js";

export interface LogEntry {
  position: number;
  author: string;
  sequence: number;
  plaintext: Uint8Array;
}
export type Reduce = (state: unknown, entry: LogEntry) => unknown | Promise<unknown>;
interface Cursor { sequence: number; previous: string }
interface Snap { format: number; log: string; through: number; state: unknown; cursors: Record<string, Cursor> }

export class DriveLog {
  private constructor(
    readonly files: DriveFiles,
    readonly file: OpenFile,
    private state: unknown,
    private through: number,
    private cursors: Record<string, Cursor>,
    private readonly reduce: Reduce,
  ) {}
  static async open(files: DriveFiles, file: OpenFile, reduce: Reduce, initial: unknown = null): Promise<DriveLog> {
    if (file.manifest.mode !== "append") throw new Error("not an append file");
    const log = new DriveLog(files, file, initial, 0, {}, reduce);
    const info = await files.client.node(file.manifest.node);
    let from = 1;
    if (info.trim_snapshot) {
      const snapFile = await files.open(info.trim_snapshot.node);
      const doc = JSON.parse(new TextDecoder().decode(await collect(files.read(snapFile)))) as Snap;
      if (doc.format !== 1 || doc.log !== file.manifest.node || doc.through + 1 !== info.trimmed_before) throw new Error("trim snapshot does not match the log");
      log.state = doc.state;
      log.through = doc.through;
      log.cursors = doc.cursors ?? {};
      from = info.trimmed_before ?? 1;
    }
    if ((info.position ?? 0) >= from) {
      for (const record of await files.tail(file, from)) await log.fold(record);
    }
    const cursor = await files.authorCursor(file);
    if (cursor.sequence > 0) log.cursors[files.client.signer.identity] = cursor;
    return log;
  }
  get value(): unknown { return this.state; }
  get position(): number { return this.through; }
  private async fold(record: { position: number; author: string; sequence: number; plain: Uint8Array }): Promise<void> {
    this.state = await this.reduce(this.state, { position: record.position, author: record.author, sequence: record.sequence, plaintext: record.plain });
    this.through = record.position;
  }
  async append(plaintext: Uint8Array): Promise<void> {
    const position = await this.files.append(this.file, plaintext);
    const cursor = await this.files.authorCursor(this.file);
    this.cursors[this.files.client.signer.identity] = cursor;
    this.state = await this.reduce(this.state, { position, author: this.files.client.signer.identity, sequence: cursor.sequence, plaintext });
    this.through = position;
  }
  /** Writes the folded state and trims records through it. Replaces an existing snapshot name. */
  async snapshot(parent: OpenFile, name: string): Promise<OpenFile> {
    const doc: Snap = { format: 1, log: this.file.manifest.node, through: this.through, state: this.state, cursors: this.cursors };
    const bytes = new TextEncoder().encode(JSON.stringify(doc));
    name = normalizeName(name);
    let file = (await this.files.list(parent)).find(child => child.name === name);
    if (file) await this.files.replace(file, bytes);
    else file = await this.files.create(parent, name, "file", bytes);
    if (this.through >= 1) {
      await this.files.client.commit({ trim: { node: this.file.manifest.node, before: this.through + 1, snapshot: { node: file.manifest.node, version: file.manifest.version } } });
    }
    return file;
  }
  async follow(onEntry?: (entry: LogEntry) => void, signal?: AbortSignal): Promise<void> {
    await this.files.client.subscribe(async event => {
      const drive = event.drive;
      if (event.type !== "drive.changed" || drive?.node !== this.file.manifest.node || drive.operation !== "append") return;
      for (const record of await this.files.tail(this.file, this.through + 1)) {
        if (record.position <= this.through) continue;
        const entry = { position: record.position, author: record.author, sequence: record.sequence, plaintext: record.plain };
        await this.fold(record);
        onEntry?.(entry);
      }
    }, signal);
  }
}
async function collect(chunks: AsyncGenerator<Uint8Array>): Promise<Uint8Array> {
  const parts: Uint8Array[] = [];
  for await (const chunk of chunks) parts.push(chunk);
  const out = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0));
  let offset = 0;
  for (const part of parts) { out.set(part, offset); offset += part.length; }
  return out;
}
