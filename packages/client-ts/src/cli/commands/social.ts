/** `poweur contacts …`, `poweur requests`, `poweur policy …`, `poweur auth …`. */

import { readFileSync } from "node:fs";
import { resolve as resolvePath } from "node:path";

import { toBase64Std } from "../../encoding.js";
import { PoweurError } from "../../errors.js";
import { clampPowBits } from "../../pow.js";
import { effectiveChallenge, effectiveMaxBytes, effectiveMaxPerDay } from "../../policy.js";
import { openClient } from "../../node/session-factory.js";
import {
  ANON_CHALLENGE_POW,
  CONTACT_ACCEPTED,
  MSG_TYPE_CONTACT_ACCEPT,
  type AnonymousPolicy,
  type InboxMode,
} from "../../types.js";
import {
  flagBool,
  flagNumber,
  flagString,
  parseArgs,
  requirePositional,
  UsageError,
} from "../args.js";
import { write, type Streams } from "../output.js";

const COMMON_BOOL = ["json"];

export async function contactsCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const args = parseArgs(argv.slice(1), { bool: COMMON_BOOL });
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const contacts = await client.contacts();

  switch (sub) {
    case "ls": {
      const file = await contacts.load();
      if (flagBool(args, "json")) return write(streams, true, file, "");
      if (file.contacts.length === 0) {
        streams.stdout("no contacts\n");
        return 0;
      }
      for (const contact of file.contacts) {
        const name = contact.petname ? `${contact.petname} (${contact.identity})` : contact.identity;
        streams.stdout(`${name}\t${contact.state}\tpinned=${Boolean(contact.pinned_key)}\n`);
      }
      return 0;
    }
    case "add": {
      const target = requirePositional(args, 0, "usage: poweur contacts add <identity>").toLowerCase();
      await contacts.set(target, CONTACT_ACCEPTED, {
        ...(flagString(args, "petname") ? { petname: flagString(args, "petname") } : {}),
      });
      streams.stdout(`added ${target}\n`);
      return 0;
    }
    case "block": {
      const target = requirePositional(args, 0, "usage: poweur contacts block <identity>").toLowerCase();
      await client.blockContact(target);
      streams.stdout(`blocked ${target}\n`);
      return 0;
    }
    case "rm": {
      const target = requirePositional(args, 0, "usage: poweur contacts rm <identity>");
      if (!(await contacts.remove(target))) {
        throw new PoweurError("not_found", `${target} is not in contacts`);
      }
      streams.stdout(`removed ${target}\n`);
      return 0;
    }
    case "request": {
      const target = requirePositional(
        args,
        0,
        "usage: poweur contacts request <identity> [<intro message>]",
      ).toLowerCase();
      try {
        await client.requestContact(target, {
          ...(args.positional[1] ? { intro: args.positional[1] } : {}),
          ...(flagString(args, "petname") ? { petname: flagString(args, "petname") } : {}),
        });
      } catch (error) {
        if (error instanceof PoweurError && error.code === "invalid_argument") {
          throw new UsageError(error.message);
        }
        throw error;
      }
      streams.stdout(`contact request sent to ${target}\n`);
      return 0;
    }
    case "accept": {
      const target = requirePositional(args, 0, "usage: poweur contacts accept <identity>").toLowerCase();
      const { notified } = await client.acceptContact(target, {
        ...(flagString(args, "petname") ? { petname: flagString(args, "petname") } : {}),
      });
      streams.stdout(`accepted ${target}\n`);
      if (!notified) {
        // Best-effort: their policy decides whether the notification lands.
        streams.stderr(
          "note: contact accepted locally, but the acceptance notification could not be sent\n",
        );
      }
      return 0;
    }
    default:
      throw new UsageError("unknown contacts subcommand (want ls, add, request, accept, block, rm)");
  }
}

export async function requestsCommand(argv: string[], streams: Streams): Promise<number> {
  const args = parseArgs(argv, { bool: COMMON_BOOL });
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });
  const requests = (await client.requests()).filter((entry) => entry.type !== MSG_TYPE_CONTACT_ACCEPT);
  if (flagBool(args, "json")) return write(streams, true, { requests }, "");
  if (requests.length === 0) {
    streams.stdout("no pending requests\n");
    return 0;
  }
  for (const request of requests) {
    streams.stdout(
      `${request.sender}\t${request.type ?? ""}\t${request.timestamp}\t` +
        `(accept with \`poweur contacts accept ${request.sender}\`)\n`,
    );
  }
  return 0;
}

export async function policyCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const args = parseArgs(argv.slice(1), { bool: [...COMMON_BOOL, "anon-allow"] });
  const { client } = await openClient({
    ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
  });

  if (sub === "show") {
    const { policy, explicit } = await client.policy();
    if (flagBool(args, "json")) return write(streams, true, policy, "");
    streams.stdout(
      `inbox policy: ${policy.mode}${explicit ? "" : " (no policy file — relay default)"}\n`,
    );
    if (policy.anonymous?.allow) {
      const challenge = effectiveChallenge(policy.anonymous);
      const bits =
        challenge === ANON_CHALLENGE_POW
          ? ` (${clampPowBits(policy.anonymous.pow_bits ?? 0)} bits)`
          : "";
      streams.stdout(
        `anonymous: allowed, challenge=${challenge}${bits}, max ${effectiveMaxBytes(policy.anonymous)} bytes, ` +
          `${effectiveMaxPerDay(policy.anonymous)}/day\n`,
      );
    } else {
      streams.stdout("anonymous: denied (default)\n");
    }
    return 0;
  }

  if (sub === "set") {
    const mode = requirePositional(
      args,
      0,
      "usage: poweur policy set <open|contacts_only|contacts_and_requests> [--anon-allow --anon-challenge=pow --anon-bits=N]",
    ) as InboxMode;
    const anonAllow = flagBool(args, "anon-allow");
    const anonChallenge = flagString(args, "anon-challenge");
    const anonBits = flagNumber(args, "anon-bits", 0);
    let anonymous: AnonymousPolicy | undefined;
    if (anonAllow || anonChallenge || anonBits > 0) {
      anonymous = {
        allow: anonAllow,
        ...(anonChallenge ? { challenge: anonChallenge } : {}),
        ...(anonBits > 0 ? { pow_bits: anonBits } : {}),
        ...(flagNumber(args, "anon-max-bytes", 0) > 0
          ? { max_bytes: flagNumber(args, "anon-max-bytes", 0) }
          : {}),
        ...(flagNumber(args, "anon-max-per-day", 0) > 0
          ? { max_per_day: flagNumber(args, "anon-max-per-day", 0) }
          : {}),
      };
    }
    const policy = await client.setPolicy(mode, anonymous);
    const note = policy.anonymous?.allow
      ? ` (anonymous allowed, challenge=${effectiveChallenge(policy.anonymous)})`
      : "";
    streams.stdout(`inbox policy set to ${policy.mode}${note}\n`);
    return 0;
  }

  throw new UsageError("unknown policy subcommand (want show, set)");
}

/** Read an auth request from a local file or a URL. */
async function readRequestPayload(source: string): Promise<string> {
  if (source.startsWith("http://") || source.startsWith("https://")) {
    const response = await fetch(source);
    if (!response.ok) {
      throw new PoweurError("relay_error", `request fetch failed with status ${response.status}`);
    }
    return response.text();
  }
  return readFileSync(resolvePath(source), "utf8");
}

export async function authCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  const args = parseArgs(argv.slice(1), { bool: COMMON_BOOL });
  const source = requirePositional(args, 0, "request file or url is required");
  const payload = await readRequestPayload(source);

  if (sub === "inspect") {
    streams.stdout(`${payload}\n`);
    return 0;
  }
  if (sub === "sign") {
    const { client } = await openClient({
      ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}),
    });
    const { signBytes } = await import("../../crypto/index.js");
    const { requireKeys } = await import("../../crypto/keys.js");
    const { FileKeyStore } = await import("../../node/keystore.js");
    const { loadConfig } = await import("../../node/config.js");
    const keys = requireKeys(
      await new FileKeyStore(loadConfig().keys_dir).load(client.identityName),
      client.identityName,
    );
    // The whole request body is signed verbatim, not a canonical projection of
    // it — the relying party checks the exact bytes it sent.
    const signature = toBase64Std(signBytes(keys.signingPrivateKey, new TextEncoder().encode(payload)));
    let requestId = "";
    try {
      requestId = String((JSON.parse(payload) as Record<string, unknown>)["request_id"] ?? "");
    } catch {
      // A non-JSON request simply has no id to echo back.
    }
    return write(
      streams,
      flagBool(args, "json"),
      {
        identity: client.identityName,
        issued_at: new Date().toISOString().replace(/\.\d{3}Z$/, "Z"),
        signature,
        request_id: requestId,
      },
      "auth request signed\n",
    );
  }
  throw new UsageError("unknown auth subcommand (want inspect, sign)");
}

export async function analyticsCommand(argv: string[], streams: Streams): Promise<number> {
  const sub = argv[0];
  if (!["show", "on", "off"].includes(sub ?? "")) throw new UsageError("usage: poweur analytics <show|on|off>");
  const args = parseArgs(argv.slice(1), { bool: COMMON_BOOL });
  const { client } = await openClient({ ...(flagString(args, "use-identity") ? { identity: flagString(args, "use-identity") } : {}) });
  const p = sub === "show" ? await client.analyticsPreference() : await client.setAnalyticsConsent(sub === "on");
  return write(streams, flagBool(args, "json"), p ?? { version: 1, granted: false }, `Detailed relay analytics: ${p?.granted ? "on (raw identity and IP)" : "off (hashed identity, no IP)"}`);
}
