/**
 * Last-known-good identity documents (EPIC-029 D1) — the twin of
 * `packages/identity/store.go`.
 *
 * The resolver writes every verified, signed document it fetches to a
 * `DocumentStore` and reads it back only when a later fetch fails for a
 * reason that says nothing about the identity (network error, timeout, 5xx).
 * `raw` is the document as it was served, so fields this build doesn't know
 * about survive a round trip.
 *
 * Stores are async because the browser one is IndexedDB. See `./node`
 * (`FileDocumentStore`, the same files the Go CLI reads) and `./browser`
 * (`IndexedDbDocumentStore`).
 */

export interface StoredDocument {
  identity: string;
  /** The document JSON as fetched. */
  raw: string;
  /** When it was fetched, in milliseconds since the epoch. */
  fetchedAt: number;
}

export interface DocumentStore {
  get(identity: string): Promise<StoredDocument | null>;
  put(identity: string, raw: string, fetchedAt: number): Promise<void>;
}

export function documentStoreKey(identity: string): string {
  return identity.trim().toLowerCase();
}

/** A store that lives as long as the page or process: tests and fallbacks. */
export class MemoryDocumentStore implements DocumentStore {
  readonly #entries = new Map<string, StoredDocument>();

  async get(identity: string): Promise<StoredDocument | null> {
    return this.#entries.get(documentStoreKey(identity)) ?? null;
  }

  async put(identity: string, raw: string, fetchedAt: number): Promise<void> {
    const key = documentStoreKey(identity);
    this.#entries.set(key, { identity: key, raw, fetchedAt });
  }
}

/**
 * Wraps a store so the resolver can fall back to it and apply the rollback
 * guard without writing; the caller stores the document itself once its own
 * check has passed.
 */
export function readOnlyDocumentStore(store: DocumentStore): DocumentStore {
  return {
    get: (identity) => store.get(identity),
    put: async () => {},
  };
}
