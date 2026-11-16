/**
 * IndexedDB-backed `DocumentStore` for the browser. It has its own database,
 * so opening it never competes with another store's schema version.
 *
 * IndexedDB can be missing or refuse to open (private windows, blocked site
 * data, a worker without it). The store then keeps documents in memory for
 * the life of the page instead of failing resolution.
 */

import {
  documentStoreKey,
  MemoryDocumentStore,
  type DocumentStore,
  type StoredDocument,
} from "../document-store.js";

export class IndexedDbDocumentStore implements DocumentStore {
  readonly #fallback = new MemoryDocumentStore();
  #broken = false;

  constructor(
    private readonly database = "poweur-identities",
    private readonly store = "documents",
  ) {}

  #open(): Promise<IDBDatabase> {
    return new Promise((resolve, reject) => {
      const request = indexedDB.open(this.database, 1);
      request.onupgradeneeded = () => request.result.createObjectStore(this.store);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
      request.onblocked = () => reject(new Error("identity document store is blocked"));
    });
  }

  async #run<T>(mode: IDBTransactionMode, body: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
    const db = await this.#open();
    try {
      const tx = db.transaction(this.store, mode);
      const done = new Promise<void>((resolve, reject) => {
        tx.oncomplete = () => resolve();
        tx.onerror = () => reject(tx.error);
        tx.onabort = () => reject(tx.error);
      });
      const result = await new Promise<T>((resolve, reject) => {
        const request = body(tx.objectStore(this.store));
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
      await done;
      return result;
    } finally {
      db.close();
    }
  }

  async get(identity: string): Promise<StoredDocument | null> {
    const key = documentStoreKey(identity);
    if (!this.#broken) {
      try {
        const found = await this.#run<StoredDocument | undefined>("readonly", (s) => s.get(key));
        if (
          found &&
          typeof found.raw === "string" &&
          typeof found.fetchedAt === "number" &&
          found.identity === key
        ) {
          return found;
        }
        if (found === undefined) return this.#fallback.get(key);
        return null;
      } catch {
        this.#broken = true;
      }
    }
    return this.#fallback.get(key);
  }

  async put(identity: string, raw: string, fetchedAt: number): Promise<void> {
    const key = documentStoreKey(identity);
    if (!this.#broken) {
      try {
        await this.#run("readwrite", (s) => s.put({ identity: key, raw, fetchedAt }, key));
        return;
      } catch {
        this.#broken = true;
      }
    }
    await this.#fallback.put(key, raw, fetchedAt);
  }
}
