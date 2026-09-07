/**
 * DNS TXT lookups via `node:dns`. This is the resolver that gives the Node
 * CLI true parity with the Go CLI — including `DNS_SERVER`, which routes
 * every lookup through a chosen server to defeat a LAN resolver that drops
 * TXT records or caches a stale NXDOMAIN.
 */

import { Resolver, promises as dnsPromises } from "node:dns";

import type { TxtResolver } from "../resolve.js";

function flatten(records: string[][]): string[] {
  // A TXT record can be split into multiple strings; DNS semantics say to
  // concatenate them, not to treat them as separate records.
  return records.map((chunks) => chunks.join(""));
}

/** TXT resolver over the system resolver, or `DNS_SERVER` when set. */
export function nodeTxtResolver(server?: string): TxtResolver {
  const target = (server ?? process.env["DNS_SERVER"] ?? "").trim();
  if (target === "") {
    return {
      async lookupTxt(name: string): Promise<string[]> {
        return flatten(await dnsPromises.resolveTxt(name));
      },
    };
  }
  const [host, port] = target.includes(":") ? target.split(":") : [target, "53"];
  const resolver = new Resolver();
  resolver.setServers([`${host}:${port ?? "53"}`]);
  return {
    lookupTxt(name: string): Promise<string[]> {
      return new Promise((resolve, reject) => {
        resolver.resolveTxt(name, (error, records) => {
          if (error) reject(error);
          else resolve(flatten(records));
        });
      });
    },
  };
}
