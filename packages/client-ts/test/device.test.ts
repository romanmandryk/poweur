import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { deviceHeaders, devicePath, loadDevice, platformName } from "../src/node/device.js";

describe("local device record", () => {
  let home: string;
  let saved: string | undefined;
  beforeEach(() => {
    saved = process.env["POWEUR_HOME"];
    home = mkdtempSync(join(tmpdir(), "poweur-device-"));
    process.env["POWEUR_HOME"] = home;
  });
  afterEach(() => {
    if (saved === undefined) delete process.env["POWEUR_HOME"];
    else process.env["POWEUR_HOME"] = saved;
  });

  it("creates device.json once and keeps the fingerprint", () => {
    const first = loadDevice();
    expect(first.fingerprint.length).toBeGreaterThan(20);
    expect(JSON.parse(readFileSync(devicePath(), "utf8")).fingerprint).toBe(first.fingerprint);
    expect(loadDevice().fingerprint).toBe(first.fingerprint);
  });

  it("adopts a record the Go CLI wrote", () => {
    writeFileSync(devicePath(), JSON.stringify({ fingerprint: "from-go", name: "MBP4.local", kind: "laptop" }));
    expect(deviceHeaders()).toMatchObject({
      "X-Poweur-Device": "from-go",
      "X-Poweur-Device-Name": "MBP4.local",
      "X-Poweur-Device-Kind": "laptop",
      "X-Poweur-Device-Client": "cli",
    });
  });

  it("names the platform the way the registry shows it", () => {
    expect(platformName("darwin")).toBe("macOS");
    expect(platformName("win32")).toBe("Windows");
    expect(platformName("freebsd")).toBe("freebsd");
  });
});
