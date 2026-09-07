/**
 * `~/.poweur/sessions/<identity>.toml` — the Go CLI's session records, in the
 * same TOML shape so a session registered by either client is usable by both.
 */

import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { rfc3339 } from "../encoding.js";
import type { SessionStore, StoredSession } from "../session.js";
import { sessionsDir } from "./paths.js";
import { parseToml, stringifyToml, tomlDate, tomlString } from "./toml.js";

export class FileSessionStore implements SessionStore {
  readonly dir: string;

  constructor(dir: string = sessionsDir()) {
    this.dir = dir;
  }

  #path(identity: string): string {
    return join(this.dir, `${identity}.toml`);
  }

  async load(identity: string): Promise<StoredSession | null> {
    const path = this.#path(identity);
    if (!existsSync(path)) return null;
    const table = parseToml(readFileSync(path, "utf8"));
    const issuedAt = tomlDate(table, "issued_at");
    const expiresAt = tomlDate(table, "expires_at");
    return {
      identity: tomlString(table, "identity", identity),
      sessionId: tomlString(table, "session_id"),
      sessionPrivateKey: tomlString(table, "session_private_key"),
      sessionPublicKey: tomlString(table, "session_public_key"),
      // The `*_raw` fields carry the exact strings that went into the
      // registration signature; the parsed dates are only for display.
      issuedAt: tomlString(table, "issued_at_raw") || (issuedAt ? rfc3339(issuedAt) : ""),
      expiresAt: tomlString(table, "expires_at_raw") || (expiresAt ? rfc3339(expiresAt) : ""),
      nonce: tomlString(table, "nonce"),
      identitySignature: tomlString(table, "identity_signature"),
      relayUrl: tomlString(table, "relay_url"),
    };
  }

  async save(session: StoredSession): Promise<void> {
    mkdirSync(this.dir, { recursive: true, mode: 0o700 });
    writeFileSync(
      this.#path(session.identity),
      stringifyToml({
        identity: session.identity,
        session_id: session.sessionId,
        session_private_key: session.sessionPrivateKey,
        session_public_key: session.sessionPublicKey,
        issued_at: new Date(session.issuedAt),
        expires_at: new Date(session.expiresAt),
        relay_url: session.relayUrl,
        issued_at_raw: session.issuedAt,
        expires_at_raw: session.expiresAt,
        nonce: session.nonce,
        identity_signature: session.identitySignature,
      }),
      { mode: 0o600 },
    );
  }

  async remove(identity: string): Promise<void> {
    rmSync(this.#path(identity), { force: true });
  }
}
