import { describe, expect, it } from "vitest";
import { readAnalyticsPreference, writeAnalyticsPreference, ANALYTICS_PATH } from "../src/analytics.js";
import type { SystemFiles } from "../src/systemfiles.js";

describe("analytics settings", () => {
  it("defaults to absent, round-trips both modes without a browser tracking ID", async () => {
    let value: string | null = null;
    const dav = { readOptional: async () => value, writeJson: async (path: string, doc: unknown) => { expect(path).toBe(ANALYTICS_PATH); value = JSON.stringify(doc); } } as unknown as SystemFiles;
    expect(await readAnalyticsPreference(dav)).toBeNull();
    for (const granted of [true, false]) {
      await writeAnalyticsPreference(dav, granted);
      expect(await readAnalyticsPreference(dav)).toMatchObject({ version: 1, granted });
    }
  });
  it("rejects malformed consent rather than enabling it", async () => {
    for (const raw of ['{}', '{"version":1,"granted":"true"}', '{"version":2,"granted":true,"updated_at":"2026-09-10T00:00:00Z"}']) {
      const dav = { readOptional: async () => raw } as unknown as SystemFiles;
      await expect(readAnalyticsPreference(dav)).rejects.toThrow();
    }
  });
});
