/**
 * WebDAV file access — the twin of `poweur dav` and `apps/web/js/files.js`.
 *
 * DAV bearer tokens are minted by signing a canonical string with the
 * identity key, so the relay never needs a password and a token can be scoped
 * to one path (`dav:rw:<path>`) or one audience (someone else's tree, when a
 * grant allows it).
 */

import { canonicalDavToken } from "./canonical.js";
import type { Signer } from "./crypto/keys.js";
import { rfc3339 } from "./encoding.js";
import { PoweurError, RelayError } from "./errors.js";
import { RelayClient, trimRelayUrl } from "./http.js";
import { newNonce } from "./ids.js";
import type { DavEntry, DavTokenResponse, QuotaResponse } from "./types.js";

/** Tree roots and who can see them (files/storage-model.md). */
export const ROOT_INFO: Record<string, { badge: string; desc: string }> = {
  "poweur-sys": { badge: "system", desc: "managed system data" },
  public: { badge: "any ID", desc: "readable by any Poweur identity" },
  shared: { badge: "grants", desc: "shared per explicit grant" },
  private: { badge: "private", desc: "only you" },
  apps: { badge: "apps", desc: "per-application data" },
};

/**
 * Mint a DAV bearer token. Defaults mirror the CLI: your own tree gets
 * `dav:full`, someone else's gets `dav:read`.
 */
export async function mintDavToken(
  client: RelayClient,
  signer: Signer,
  options: { audience?: string; scope?: string } = {},
): Promise<DavTokenResponse> {
  const audience = options.audience || signer.identity;
  const scope =
    options.scope ||
    (audience.toLowerCase() === signer.identity.toLowerCase() ? "dav:full" : "dav:read");
  const issuedAt = rfc3339();
  const nonce = newNonce();
  const signature = await signer.sign(
    canonicalDavToken(signer.identity, audience, scope, issuedAt, nonce),
    "base64url",
  );
  return client.request<DavTokenResponse>({
    method: "POST",
    path: "/auth/dav-token",
    body: {
      identity: signer.identity,
      audience,
      scope,
      issued_at: issuedAt,
      nonce,
      signature,
    },
  });
}

function davPath(path: string): string {
  return String(path ?? "")
    .replace(/^\/+/, "")
    .split("/")
    .map(encodeURIComponent)
    .join("/");
}

/** Absolute URL for a path in an identity's tree. */
export function davUrl(relayUrl: string, identity: string, path: string): string {
  return `${trimRelayUrl(relayUrl)}/dav/${encodeURIComponent(identity)}/${davPath(path)}`;
}

/** Bearer-authenticated DAV access to one identity's tree. */
export class DavClient {
  readonly client: RelayClient;
  readonly identity: string;
  readonly token: string;

  constructor(client: RelayClient, identity: string, token: string) {
    this.client = client;
    this.identity = identity;
    this.token = token;
  }

  /** Mint a token and return a ready client (the common entry point). */
  static async connect(
    client: RelayClient,
    signer: Signer,
    options: { audience?: string; scope?: string } = {},
  ): Promise<DavClient> {
    const token = await mintDavToken(client, signer, options);
    return new DavClient(client, token.audience || signer.identity, token.token);
  }

  async #fetch(
    method: string,
    path: string,
    init: { body?: BodyInit; headers?: Record<string, string> } = {},
  ): Promise<Response> {
    const response = await this.client.raw({
      method,
      path: `/dav/${encodeURIComponent(this.identity)}/${davPath(path)}`,
      ...(init.body !== undefined ? { body: init.body } : {}),
      headers: { Authorization: `Bearer ${this.token}`, ...init.headers },
    });
    if (!response.ok && response.status !== 207) {
      const text = await response.text().catch(() => "");
      let detail = text;
      try {
        const parsed = JSON.parse(text) as Record<string, unknown>;
        detail = String(parsed["detail"] ?? parsed["error"] ?? text);
      } catch {
        // Non-JSON body: the raw text is the best detail available.
      }
      throw new RelayError(detail || `${method} ${path}: HTTP ${response.status}`, {
        status: response.status,
        detail,
      });
    }
    return response;
  }

  /** PROPFIND depth 1. */
  async list(path = ""): Promise<DavEntry[]> {
    const response = await this.#fetch("PROPFIND", path ? `${path}/` : "", {
      headers: { Depth: "1" },
    });
    return parseMultistatus(await response.text(), path);
  }

  async readText(path: string): Promise<string> {
    return (await this.#fetch("GET", path)).text();
  }

  async readBytes(path: string): Promise<Uint8Array> {
    return new Uint8Array(await (await this.#fetch("GET", path)).arrayBuffer());
  }

  /** GET returning null on 404 — the shape most poweur-sys readers want. */
  async readOptional(path: string): Promise<string | null> {
    try {
      return await this.readText(path);
    } catch (error) {
      if (error instanceof RelayError && error.status === 404) return null;
      throw error;
    }
  }

  async write(path: string, body: string | Uint8Array | Blob): Promise<void> {
    await this.#fetch("PUT", path, { body: body as BodyInit });
  }

  async writeJson(path: string, value: unknown): Promise<void> {
    await this.write(path, `${JSON.stringify(value, null, 2)}\n`);
  }

  async mkdir(path: string): Promise<void> {
    await this.#fetch("MKCOL", path);
  }

  async move(from: string, to: string): Promise<void> {
    await this.#fetch("MOVE", from, {
      headers: {
        Destination: davUrl(this.client.relayUrl, this.identity, to),
        Overwrite: "F",
      },
    });
  }

  /** DELETE. Returns false when the path was already gone. */
  async remove(path: string): Promise<boolean> {
    try {
      await this.#fetch("DELETE", path);
      return true;
    } catch (error) {
      if (error instanceof RelayError && error.status === 404) return false;
      throw error;
    }
  }

  async quota(): Promise<QuotaResponse> {
    const response = await this.client.raw({
      method: "GET",
      path: `/files/${encodeURIComponent(this.identity)}/quota`,
      headers: { Authorization: `Bearer ${this.token}` },
    });
    if (!response.ok) {
      throw new PoweurError("relay_error", `quota failed: HTTP ${response.status}`, {
        status: response.status,
      });
    }
    return (await response.json()) as QuotaResponse;
  }
}

function decodeXml(value: string): string {
  return value
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, code: string) => String.fromCharCode(Number(code)))
    .replace(/&amp;/g, "&");
}

/**
 * Parse a WebDAV multistatus body without DOMParser, so the same code runs in
 * Node. The requested collection itself is excluded from the result.
 */
export function parseMultistatus(xml: string, requestPath = ""): DavEntry[] {
  const entries: DavEntry[] = [];
  const chunks = xml.split(/<\/D:response>/i).slice(0, -1);
  const requested =
    `/${String(requestPath).replace(/^\/+|\/+$/g, "")}`.replace(/\/$/, "") || "/";

  for (const chunk of chunks) {
    const hrefMatch = /<D:href>([^<]*)<\/D:href>/i.exec(chunk);
    if (!hrefMatch) continue;
    let href = decodeXml(hrefMatch[1] as string);
    try {
      href = decodeURIComponent(href);
    } catch {
      // A malformed escape leaves the raw href, which still yields a usable name.
    }
    const treePath = href.replace(/^.*?\/dav\/[^/]+/, "").replace(/\/$/, "") || "/";
    if (treePath === requested) continue;

    const sizeMatch = /<D:getcontentlength>(\d+)<\/D:getcontentlength>/i.exec(chunk);
    const modifiedMatch = /<D:getlastmodified>([^<]*)<\/D:getlastmodified>/i.exec(chunk);
    const etagMatch = /<D:getetag>([^<]*)<\/D:getetag>/i.exec(chunk);
    entries.push({
      name: treePath.split("/").filter(Boolean).pop() ?? treePath,
      path: treePath.replace(/^\//, ""),
      dir: /<D:resourcetype>\s*<D:collection[^>]*>/i.test(chunk),
      size: sizeMatch ? Number(sizeMatch[1]) : 0,
      modified: modifiedMatch ? decodeXml(modifiedMatch[1] as string) : "",
      etag: etagMatch ? decodeXml(etagMatch[1] as string) : "",
    });
  }

  entries.sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
  return entries;
}

/** Human-readable byte size, matching the web client's formatting. */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(units.length - 1, Math.floor(Math.log2(n) / 10));
  const value = n / 2 ** (10 * index);
  return `${value >= 10 || index === 0 ? Math.round(value) : value.toFixed(1)} ${units[index]}`;
}
