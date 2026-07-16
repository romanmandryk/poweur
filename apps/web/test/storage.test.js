/**
 * @vitest-environment happy-dom
 *
 * Unit parity with apps/cli TestIdentityCreateWritesConfig (local persistence).
 */
import { describe, it, expect, beforeEach } from "vitest";
import {
  saveConfig,
  getConfig,
  saveIdentityRecord,
  loadIdentityRecord,
  listIdentities,
  setActiveIdentity,
  getActiveIdentity,
  saveSessionRecord,
  loadSessionRecord,
  isSessionValid,
} from "../js/storage.js";

describe("storage unit (CLI config/keys parity)", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  it("saves config and identity record after create", () => {
    const identity = "alice.poweur.net";
    saveConfig({
      relayUrl: "http://127.0.0.1:9999",
      parentDomain: "poweur.net",
      dnsProvider: "cloudflare",
    });
    saveIdentityRecord(identity, {
      identity,
      publicKey: "pub",
      encPublicKey: "enc",
      credentialId: "cred",
      encryptedKeys: { kdf: "pbkdf2", iv: "i", ciphertext: "c", salt: "s" },
      relay: "http://127.0.0.1:9999",
      userId: "u",
      createdAt: "2026-01-01T00:00:00Z",
      supportsPRF: false,
    });
    setActiveIdentity(identity);

    const cfg = getConfig();
    expect(cfg.relayUrl).toBe("http://127.0.0.1:9999");
    expect(cfg.parentDomain).toBe("poweur.net");
    expect(loadIdentityRecord(identity)?.publicKey).toBe("pub");
    expect(loadIdentityRecord(identity)?.encPublicKey).toBe("enc");
    expect(listIdentities()).toEqual([identity]);
    expect(getActiveIdentity()).toBe(identity);
  });

  it("persists session in sessionStorage with TTL", () => {
    const identity = "alice.poweur.net";
    const expiresAt = new Date(Date.now() + 23 * 3600_000).toISOString();
    saveSessionRecord(identity, {
      sessionId: "sess_test",
      sessionPublicKey: "pk",
      sessionSigningJWK: { kty: "OKP" },
      issuedAt: new Date().toISOString(),
      expiresAt,
    });
    const sess = loadSessionRecord(identity);
    expect(sess.sessionId).toBe("sess_test");
    expect(isSessionValid(identity)).toBe(true);
  });
});
