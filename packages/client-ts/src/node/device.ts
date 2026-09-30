/**
 * This machine's identity in the owner's device list (EPIC-004 E04-T6).
 *
 * `~/.poweur/device.json` is shared with the Go CLI — same fields, same
 * meaning — so a machine running both is one row, not two. The fingerprint is
 * random and machine-local: the relay stores only its hash.
 */

import { randomBytes } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { hostname } from "node:os";
import { dirname, join } from "node:path";
import { poweurHome } from "./paths.js";

export interface LocalDevice {
  fingerprint: string;
  name?: string;
  kind?: string;
}

export function devicePath(): string {
  return join(poweurHome(), "device.json");
}

/** The platform label the registry shows ("macOS", "Linux", "Windows"). */
export function platformName(platform: string = process.platform): string {
  switch (platform) {
    case "darwin":
      return "macOS";
    case "linux":
      return "Linux";
    case "win32":
      return "Windows";
    default:
      return platform;
  }
}

/** Read the local device record, creating it on first use. */
export function loadDevice(): LocalDevice {
  const path = devicePath();
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8")) as LocalDevice;
    if (typeof parsed.fingerprint === "string" && parsed.fingerprint.trim() !== "") return parsed;
  } catch {
    // missing or unreadable: make a fresh one
  }
  const device: LocalDevice = {
    fingerprint: randomBytes(24).toString("base64url"),
    name: (process.env["POWEUR_DEVICE_NAME"] || hostname()).trim(),
    kind: process.env["POWEUR_DEVICE_KIND"] || (process.stdin.isTTY ? "laptop" : "agent"),
  };
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(path, JSON.stringify(device, null, 2) + "\n", { mode: 0o600 });
  return device;
}

/**
 * The optional device headers for every relay request. Never fatal: a client
 * that cannot build them is just anonymous.
 */
export function deviceHeaders(): Record<string, string> {
  try {
    const d = loadDevice();
    const headers: Record<string, string> = {
      "X-Poweur-Device": d.fingerprint,
      "X-Poweur-Device-Client": "cli",
      "X-Poweur-Device-Platform": platformName(),
    };
    if (d.name) headers["X-Poweur-Device-Name"] = d.name;
    if (d.kind) headers["X-Poweur-Device-Kind"] = d.kind;
    return headers;
  } catch {
    return {};
  }
}
