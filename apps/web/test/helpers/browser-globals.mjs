/**
 * The browser globals `apps/web`'s modules need, for Node test runs.
 *
 * These suites want the real modules *and* Node's real `fetch` (they talk to a
 * real relay), so switching the whole file to a DOM environment would trade one
 * problem for another. Installing the two or three globals they actually touch
 * is smaller and keeps the network real.
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

// WebAuthn binds credentials to the page's host, so `rpId()` reads it. Node has
// no page; stand in for one.
if (!globalThis.location) {
  globalThis.location = { hostname: "poweur.net", origin: "https://poweur.net" };
}
if (!globalThis.window) globalThis.window = globalThis;

export function resetWebStorage() {
  globalThis.localStorage.clear();
  globalThis.sessionStorage.clear();
}
