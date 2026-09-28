import { lstat, mkdir, readFile, rename, unlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { chunkID, MAX_CHUNK_BYTES } from "../drive/crypto.js";
import type { ChunkCache } from "../drive/client.js";
import { poweurHome } from "./paths.js";

/** Ciphertext-only cache, shared with the Go CLI. */
export class FileChunkCache implements ChunkCache {
  constructor(readonly directory = join(poweurHome(), "cache", "drive-chunks")) {}
  private path(id: string): string {
    if (!/^[0-9a-f]{64}$/.test(id)) throw new Error("invalid chunk ID");
    return join(this.directory, id);
  }
  async get(id: string): Promise<Uint8Array | null> {
    const path = this.path(id);
    try {
      const stat = await lstat(path);
      if (!stat.isFile() || stat.size > MAX_CHUNK_BYTES) return null;
      const bytes = new Uint8Array(await readFile(path));
      return chunkID(bytes) === id ? bytes : null;
    } catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return null; throw error; }
  }
  async put(id: string, bytes: Uint8Array): Promise<void> {
    const path = this.path(id);
    if (bytes.length > MAX_CHUNK_BYTES || chunkID(bytes) !== id) throw new Error("invalid cached chunk");
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const temporary = join(this.directory, `.chunk-${randomUUID()}`);
    try { await writeFile(temporary, bytes, { mode: 0o600, flag: "wx" }); await rename(temporary, path); }
    finally { await unlink(temporary).catch(error => { if (error.code !== "ENOENT") throw error; }); }
  }
}
