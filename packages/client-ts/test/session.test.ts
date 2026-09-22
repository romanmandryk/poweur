import { describe, expect, it } from "vitest";

import { SESSION_TTL_MS, sessionValidityWindow } from "../src/session.js";

describe("sessionValidityWindow", () => {
  it("stays at exactly 24h when the clock is just under the next second", () => {
    const now = new Date("2026-01-15T12:00:00.999Z");
    const { issuedAt, expiresAt } = sessionValidityWindow(now);
    expect(issuedAt).toBe("2026-01-15T12:00:00Z");
    expect(expiresAt).toBe("2026-01-16T12:00:00Z");
    expect(Date.parse(expiresAt) - Date.parse(issuedAt)).toBe(SESSION_TTL_MS);
  });

  it("does not grow past 24h if a millisecond ticks between the two stamps", () => {
    // The old register() path called Date.now() twice. Crossing :00.999 → :01.000
    // made expires_at 24h+1s after RFC3339 truncation, which the relay rejects.
    const almost = new Date("2026-01-15T12:00:00.999Z");
    const next = new Date("2026-01-15T12:00:01.000Z");
    const fromOneClock = sessionValidityWindow(almost);
    expect(Date.parse(fromOneClock.expiresAt) - Date.parse(fromOneClock.issuedAt)).toBe(
      SESSION_TTL_MS,
    );
    const splitClocks =
      Date.parse(sessionValidityWindow(next).expiresAt) - Date.parse(sessionValidityWindow(almost).issuedAt);
    expect(splitClocks).toBe(SESSION_TTL_MS + 1000);
  });
});
