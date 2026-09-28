import { chunkID, MAX_CHUNK_BYTES } from "../drive/crypto.js";
import type { ChunkCache } from "../drive/client.js";

/**
 * Immutable ciphertext cache. A hit is never revalidated against the relay;
 * the chunk id is the content hash, so a matching record is the bytes.
 */
export class IndexedDBChunkCache implements ChunkCache {
  constructor(private readonly database = "poweur", private readonly store = "drive-chunks") {}
  private open(): Promise<IDBDatabase> {
    return new Promise((resolve, reject) => {
      const request = indexedDB.open(this.database, 1);
      request.onupgradeneeded = () => request.result.createObjectStore(this.store);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  }
  private async run<T>(mode: IDBTransactionMode, body: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
    const db = await this.open();
    try {
      const tx = db.transaction(this.store, mode);
      const result = await new Promise<T>((resolve, reject) => {
        const request = body(tx.objectStore(this.store));
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
      await new Promise<void>((resolve, reject) => {
        tx.oncomplete = () => resolve();
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
      });
      return result;
    } finally { db.close(); }
  }
  async get(id: string): Promise<Uint8Array | null> {
    if (!/^[0-9a-f]{64}$/.test(id)) throw new Error("invalid chunk ID");
    const stored = await this.run<ArrayBuffer | undefined>("readonly", store => store.get(id));
    if (!stored) return null;
    const bytes = new Uint8Array(stored);
    return bytes.length <= MAX_CHUNK_BYTES && chunkID(bytes) === id ? bytes : null;
  }
  async put(id: string, bytes: Uint8Array): Promise<void> {
    if (!/^[0-9a-f]{64}$/.test(id) || bytes.length > MAX_CHUNK_BYTES || chunkID(bytes) !== id) throw new Error("invalid cached chunk");
    const copy = new Uint8Array(bytes);
    await this.run("readwrite", store => store.put(copy.buffer, id));
  }
}
