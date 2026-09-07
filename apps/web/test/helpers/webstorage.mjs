/**
 * Minimal Web Storage for Node test runs.
 *
 * `js/storage.js` is a browser module; the relay-backed suites need its real
 * behaviour but also need Node's real `fetch` to reach a real relay, so we
 * install just the two globals rather than switching the whole file to a DOM
 * environment.
 */
class MemoryStorage {
  #entries = new Map();

  get length() { return this.#entries.size; }
  key(i) { return [...this.#entries.keys()][i] ?? null; }
  getItem(k) { return this.#entries.has(String(k)) ? this.#entries.get(String(k)) : null; }
  setItem(k, v) { this.#entries.set(String(k), String(v)); }
  removeItem(k) { this.#entries.delete(String(k)); }
  clear() { this.#entries.clear(); }
}

if (!globalThis.localStorage) globalThis.localStorage = new MemoryStorage();
if (!globalThis.sessionStorage) globalThis.sessionStorage = new MemoryStorage();

export function resetWebStorage() {
  globalThis.localStorage.clear();
  globalThis.sessionStorage.clear();
}
