/**
 * Last-seen state kept on this device (Files listings, message history), so a
 * screen shows at once and then refreshes from the relay. Encrypted at rest
 * with a key derived from the identity's own encryption key: readable only
 * once the identity is unlocked, like everything else on the device. Best
 * effort throughout — a missing or unreadable snapshot just means a cold load.
 */
import { signerFor } from "./client.js";

const DB = "poweur-snapshots";
const STORE = "snapshots";
const keys = new Map<string, Promise<CryptoKey>>();

function db(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB, 1);
    request.onupgradeneeded = () => request.result.createObjectStore(STORE);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function keyFor(identity: string): Promise<CryptoKey> {
  // Locked again: forget the derived key too.
  if (!signerFor(identity)?.decryptor) {
    keys.delete(identity);
    return Promise.reject(new Error("locked"));
  }
  let key = keys.get(identity);
  if (!key) {
    key = (async () => {
      const decryptor = signerFor(identity)?.decryptor;
      if (!decryptor) throw new Error("locked");
      const secret = await crypto.subtle.importKey("raw", Uint8Array.from(await decryptor.privateKeyBytes()), "HKDF", false, ["deriveKey"]);
      return crypto.subtle.deriveKey(
        { name: "HKDF", hash: "SHA-256", salt: new Uint8Array(32), info: new TextEncoder().encode(`poweur/web-snapshot/v1:${identity}`) },
        secret, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
    })();
    keys.set(identity, key);
    key.catch(() => keys.delete(identity));
  }
  return key;
}

async function tx<T>(mode: IDBTransactionMode, run: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const database = await db();
  try {
    return await new Promise<T>((resolve, reject) => {
      const request = run(database.transaction(STORE, mode).objectStore(STORE));
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
  } finally {
    database.close();
  }
}

/** Store `value` as this identity's `name` snapshot. */
export async function saveSnapshot(identity: string, name: string, value: unknown): Promise<void> {
  try {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv, additionalData: new TextEncoder().encode(name) },
      await keyFor(identity), new TextEncoder().encode(JSON.stringify(value)));
    await tx("readwrite", (store) => store.put({ iv, ciphertext }, `${identity}:${name}`));
  } catch {
    // A device without IndexedDB, or a locked identity: no snapshot.
  }
}

/** This identity's `name` snapshot, or null. */
export async function loadSnapshot<T>(identity: string, name: string): Promise<T | null> {
  try {
    const row = await tx<{ iv: Uint8Array; ciphertext: ArrayBuffer } | undefined>("readonly", (store) => store.get(`${identity}:${name}`));
    if (!row) return null;
    const plain = await crypto.subtle.decrypt({ name: "AES-GCM", iv: Uint8Array.from(row.iv), additionalData: new TextEncoder().encode(name) },
      await keyFor(identity), row.ciphertext);
    return JSON.parse(new TextDecoder().decode(plain)) as T;
  } catch {
    return null;
  }
}

/** Forget every snapshot of an identity (sign-out, identity removal). */
export async function clearSnapshots(identity: string): Promise<void> {
  keys.delete(identity);
  try {
    await tx("readwrite", (store) => store.delete(IDBKeyRange.bound(`${identity}:`, `${identity}:￿`)));
  } catch {
    // Nothing stored.
  }
}
