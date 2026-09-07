/**
 * The `~/.poweur` tree is shared with the Go CLI, so these tests pin the
 * on-disk formats: TOML round-trips, key file encoding, journal collapse.
 */

import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { generateIdentityKeys } from "../src/crypto/keys.js";
import { ed25519PublicKey } from "../src/crypto/index.js";
import { fromBase64 } from "../src/encoding.js";
import { loadConfig, saveConfig } from "../src/node/config.js";
import {
  appendJournal,
  journalStatuses,
  STATE_DELIVERED_CLIENT,
  STATE_DELIVERED_RECIPIENT_RELAY,
  STATE_FAILED,
  STATE_QUEUED,
  tickGlyph,
} from "../src/node/journal.js";
import { FileKeyStore } from "../src/node/keystore.js";
import { FileSessionStore } from "../src/node/sessionstore.js";
import { parseToml, stringifyToml } from "../src/node/toml.js";
import { Ignore } from "../src/node/syncengine.js";

let home: string;
const originalHome = process.env["POWEUR_HOME"];

beforeEach(() => {
  home = mkdtempSync(join(tmpdir(), "poweur-store-"));
  process.env["POWEUR_HOME"] = home;
});

afterEach(() => {
  if (originalHome === undefined) delete process.env["POWEUR_HOME"];
  else process.env["POWEUR_HOME"] = originalHome;
  rmSync(home, { recursive: true, force: true });
});

describe("TOML subset", () => {
  it("round-trips the value kinds the Go CLI writes", () => {
    const source = stringifyToml({
      relay_url: "https://poweur.net",
      via_home_relay: true,
      count: 42,
      issued_at: new Date("2026-01-15T09:30:00.000Z"),
    });
    const parsed = parseToml(source);
    expect(parsed["relay_url"]).toBe("https://poweur.net");
    expect(parsed["via_home_relay"]).toBe(true);
    expect(parsed["count"]).toBe(42);
    expect(parsed["issued_at"]).toBeInstanceOf(Date);
  });

  it("writes datetimes unquoted so Go can unmarshal them as time.Time", () => {
    const source = stringifyToml({ issued_at: new Date("2026-01-15T09:30:00.000Z") });
    expect(source.trim()).toBe("issued_at = 2026-01-15T09:30:00Z");
  });

  it("reads what go-toml writes, comments and sections included", () => {
    const parsed = parseToml(
      [
        "# a comment",
        "relay_url = 'https://poweur.net'",
        'identity = "alice.poweur.net"   # trailing comment',
        "via_home_relay = false",
        "[section]",
        "nested = 1",
      ].join("\n"),
    );
    expect(parsed["relay_url"]).toBe("https://poweur.net");
    expect(parsed["identity"]).toBe("alice.poweur.net");
    expect(parsed["via_home_relay"]).toBe(false);
    expect(parsed["section.nested"]).toBe(1);
  });

  it("does not truncate a value containing a #", () => {
    expect(parseToml('note = "a # b"')["note"]).toBe("a # b");
  });
});

describe("config", () => {
  it("round-trips through ~/.poweur/config.toml", () => {
    saveConfig({
      relay_url: "https://poweur.net",
      identity: "alice.poweur.net",
      keys_dir: join(home, "keys"),
      parent_domain: "poweur.net",
      via_home_relay: true,
    });
    const loaded = loadConfig();
    expect(loaded.identity).toBe("alice.poweur.net");
    expect(loaded.relay_url).toBe("https://poweur.net");
    expect(loaded.via_home_relay).toBe(true);
  });

  it("falls back to the default keys dir when the file omits one", () => {
    writeFileSync(join(home, "config.toml"), 'identity = "alice.poweur.net"\n');
    expect(loadConfig().keys_dir).toBe(join(home, "keys"));
  });

  it("returns defaults when no config file exists", () => {
    expect(loadConfig().identity).toBe("");
  });
});

describe("key store", () => {
  it("writes the 64-byte Ed25519 form the Go CLI reads", async () => {
    const store = new FileKeyStore(join(home, "keys"));
    const keys = generateIdentityKeys("alice.poweur.net");
    await store.save(keys);

    const raw = readFileSync(join(home, "keys", "alice.poweur.net.key"), "utf8");
    const decoded = fromBase64(raw);
    expect(decoded).toHaveLength(64);
    // seed || public, exactly Go's ed25519.PrivateKey layout
    expect(decoded.subarray(32)).toEqual(ed25519PublicKey(keys.signingPrivateKey));
    expect(fromBase64(readFileSync(join(home, "keys", "alice.poweur.net.enc"), "utf8"))).toHaveLength(32);
  });

  it("loads a key written in either the 32- or 64-byte form", async () => {
    const dir = join(home, "keys");
    mkdirSync(dir, { recursive: true });
    const store = new FileKeyStore(dir);
    const keys = generateIdentityKeys("alice.poweur.net");
    await store.save(keys);
    const loaded = await store.load("alice.poweur.net");
    expect(loaded?.signingPrivateKey).toHaveLength(64);
    expect(loaded?.encryptionPrivateKey).toHaveLength(32);
  });

  it("lists and removes identities", async () => {
    const store = new FileKeyStore(join(home, "keys"));
    await store.save(generateIdentityKeys("b.poweur.net"));
    await store.save(generateIdentityKeys("a.poweur.net"));
    expect(await store.list()).toEqual(["a.poweur.net", "b.poweur.net"]);
    await store.remove("a.poweur.net");
    expect(await store.list()).toEqual(["b.poweur.net"]);
  });

  it("reports an empty list when the directory does not exist", async () => {
    expect(await new FileKeyStore(join(home, "nope")).list()).toEqual([]);
  });
});

describe("session store", () => {
  it("round-trips a session, preserving the raw signed timestamps", async () => {
    const store = new FileSessionStore(join(home, "sessions"));
    await store.save({
      identity: "alice.poweur.net",
      sessionId: "sess_0011",
      sessionPrivateKey: "AAAA",
      sessionPublicKey: "BBBB",
      issuedAt: "2026-01-15T09:30:00Z",
      expiresAt: "2026-01-16T09:30:00Z",
      nonce: "nonce",
      identitySignature: "sig",
      relayUrl: "https://poweur.net",
    });
    const loaded = await store.load("alice.poweur.net");
    // The *_raw fields are the exact strings that went into the signature —
    // reformatting them would invalidate the session proof.
    expect(loaded?.issuedAt).toBe("2026-01-15T09:30:00Z");
    expect(loaded?.expiresAt).toBe("2026-01-16T09:30:00Z");
    expect(loaded?.sessionId).toBe("sess_0011");

    const onDisk = readFileSync(join(home, "sessions", "alice.poweur.net.toml"), "utf8");
    expect(onDisk).toContain("issued_at = 2026-01-15T09:30:00Z");
    expect(onDisk).toContain('issued_at_raw = "2026-01-15T09:30:00Z"');

    await store.remove("alice.poweur.net");
    expect(await store.load("alice.poweur.net")).toBeNull();
  });
});

describe("delivery journal", () => {
  it("collapses transitions to the latest state per message", () => {
    const dir = join(home, "pending");
    const entry = { sender: "alice.poweur.net", recipient: "bob.example.org" };
    appendJournal({ ...entry, message_id: "m1", timestamp: "t1", state: STATE_QUEUED }, dir);
    appendJournal(
      { ...entry, message_id: "m1", timestamp: "t2", state: STATE_DELIVERED_RECIPIENT_RELAY },
      dir,
    );
    appendJournal({ ...entry, message_id: "m2", timestamp: "t3", state: STATE_QUEUED }, dir);
    appendJournal({ ...entry, message_id: "m1", timestamp: "t4", state: STATE_DELIVERED_CLIENT }, dir);

    const statuses = journalStatuses("alice.poweur.net", dir);
    expect(statuses.map((s) => s.message_id)).toEqual(["m1", "m2"]);
    expect(statuses[0]?.state).toBe(STATE_DELIVERED_CLIENT);
    expect(statuses[0]?.history).toHaveLength(3);
    expect(statuses[1]?.state).toBe(STATE_QUEUED);
  });

  it("keeps failure sticky — a failed message never reads as delivered", () => {
    const dir = join(home, "pending");
    const entry = { sender: "alice.poweur.net", recipient: "bob.example.org", message_id: "m1" };
    appendJournal({ ...entry, timestamp: "t1", state: STATE_FAILED, detail: "boom" }, dir);
    appendJournal({ ...entry, timestamp: "t2", state: STATE_DELIVERED_RECIPIENT_RELAY }, dir);
    expect(journalStatuses("alice.poweur.net", dir)[0]?.state).toBe(STATE_FAILED);
  });

  it("returns nothing for an identity with no journal", () => {
    expect(journalStatuses("nobody.poweur.net", join(home, "pending"))).toEqual([]);
  });

  it("renders the WhatsApp-style ticks", () => {
    expect(tickGlyph(STATE_QUEUED)).toBe(" · ");
    expect(tickGlyph(STATE_DELIVERED_RECIPIENT_RELAY)).toBe(" ✓ ");
    expect(tickGlyph(STATE_DELIVERED_CLIENT)).toBe(" ✓✓");
    expect(tickGlyph(STATE_FAILED)).toBe(" ✗ ");
  });
});

describe("ignore patterns", () => {
  it("always skips the sync bookkeeping files", () => {
    const ignore = new Ignore();
    expect(ignore.match("private/.poweur-sync.json")).toBe(true);
    expect(ignore.match(".poweurignore")).toBe(true);
    expect(ignore.match("private/notes.txt")).toBe(false);
  });

  it("matches basenames anywhere and anchored paths exactly", () => {
    const ignore = new Ignore(["*.tmp", "private/scratch/", "node_modules"]);
    expect(ignore.match("public/a.tmp")).toBe(true);
    expect(ignore.match("private/scratch/deep/file.txt")).toBe(true);
    expect(ignore.match("apps/node_modules/pkg/index.js")).toBe(true);
    expect(ignore.match("public/a.txt")).toBe(false);
    // Anchored patterns must not match the same name under another parent.
    expect(ignore.match("public/scratch/file.txt")).toBe(false);
  });
});
