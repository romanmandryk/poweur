/**
 * The device registry's presentation half (EPIC-004 E04-T6). What matters is
 * the staleness line the epic asked for — "phone last synced 3 days ago" —
 * and that a row with nothing in it still renders something honest.
 */
import { describe, it, expect } from "vitest";

import { describeDevice, describeDeviceSync, deviceIcon, DEVICE_KIND_ICON }
  from "../../src/lib/devices.js";

const NOW = Date.parse("2026-09-10T12:00:00Z");
const ago = (ms) => new Date(NOW - ms).toISOString();

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

describe("describeDeviceSync", () => {
  const cases = [
    ["never synced device", {}, "never synced"],
    ["missing synced_at", { last_seen: ago(MIN) }, "never synced"],
    ["unparseable timestamp", { synced_at: "three days ago" }, "never synced"],
    ["seconds", { synced_at: ago(10_000) }, "synced just now"],
    ["minutes", { synced_at: ago(20 * MIN) }, "synced 20 min ago"],
    ["hours", { synced_at: ago(5 * HOUR) }, "synced 5 h ago"],
    ["one day", { synced_at: ago(DAY) }, "synced 1 day ago"],
    // The line the epic named.
    ["three days", { synced_at: ago(3 * DAY) }, "synced 3 days ago"],
  ];
  for (const [name, device, want] of cases) {
    it(name, () => expect(describeDeviceSync(device, NOW)).toBe(want));
  }

  it("survives a null device", () => {
    expect(describeDeviceSync(null, NOW)).toBe("never synced");
  });

  // A clock skewed forward must not produce "synced -3 days ago".
  it("clamps a future timestamp", () => {
    expect(describeDeviceSync({ synced_at: new Date(NOW + DAY).toISOString() }, NOW))
      .toBe("synced just now");
  });
});

describe("deviceIcon", () => {
  it("has an icon for every kind devices.json accepts", () => {
    for (const kind of ["laptop", "desktop", "phone", "tablet", "browser", "agent", "unknown"]) {
      expect(DEVICE_KIND_ICON[kind]).toBeTruthy();
      expect(deviceIcon(kind)).toBe(DEVICE_KIND_ICON[kind]);
    }
  });

  it("falls back for a kind it has never heard of", () => {
    expect(deviceIcon("toaster")).toBe(DEVICE_KIND_ICON.unknown);
    expect(deviceIcon(undefined)).toBe(DEVICE_KIND_ICON.unknown);
  });
});

describe("describeDevice", () => {
  it("summarises kind, last seen, staleness and credentials", () => {
    const line = describeDevice({
      id: "dev_aaaaaaaaaaaaaaaa",
      kind: "phone",
      last_seen: ago(HOUR),
      synced_at: ago(3 * DAY),
      app_passwords: ["phone-dav"],
    }, NOW);
    expect(line).toContain("phone");
    expect(line).toContain("synced 3 days ago");
    expect(line).toContain("1 app password");
  });

  it("says so plainly when the relay has seen nothing", () => {
    expect(describeDevice({ id: "dev_aaaaaaaaaaaaaaaa" }, NOW))
      .toBe("unknown · never seen · never synced");
  });

  it("pluralises app passwords", () => {
    expect(describeDevice({ app_passwords: ["a", "b"] }, NOW)).toContain("2 app passwords");
  });

  // A revoked row names no credentials — the revocation deleted them — so it
  // must not claim otherwise.
  it("omits the credential clause when there are none", () => {
    expect(describeDevice({ revoked: true, app_passwords: [] }, NOW)).not.toContain("app password");
  });
});
