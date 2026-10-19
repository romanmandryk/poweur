import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { coalesce, touchesHistory } from "../../src/lib/historySync";

describe("touchesHistory", () => {
  it("counts writes to this identity's own drive", () => {
    const own = { type: "drive.changed", drive: { drive: "Alice.poweur.net", operation: "append" } };
    expect(touchesHistory(own, "alice.poweur.net")).toBe(true);
    expect(touchesHistory({ ...own, drive: { drive: "alice.poweur.net", operation: "replace" } }, "alice.poweur.net")).toBe(true);
  });

  it("ignores other drives and other events", () => {
    expect(touchesHistory({ type: "drive.changed", drive: { drive: "team.poweur.net", operation: "append" } }, "alice.poweur.net")).toBe(false);
    expect(touchesHistory({ type: "ready" }, "alice.poweur.net")).toBe(false);
    expect(touchesHistory({ type: "drive.changed" }, "alice.poweur.net")).toBe(false);
  });
});

describe("coalesce", () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it("runs once for a burst of triggers", async () => {
    const refresh = vi.fn(async () => {});
    const c = coalesce(refresh, 400);
    c.trigger(); c.trigger(); c.trigger();
    await vi.advanceTimersByTimeAsync(399);
    expect(refresh).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("runs once more after a trigger that arrives mid-refresh, never overlapping", async () => {
    let release!: () => void;
    let active = 0;
    let most = 0;
    const refresh = vi.fn(async () => {
      active++; most = Math.max(most, active);
      await new Promise<void>((resolve) => { release = resolve; });
      active--;
    });
    const c = coalesce(refresh, 100);
    c.trigger();
    await vi.advanceTimersByTimeAsync(100);
    expect(refresh).toHaveBeenCalledTimes(1);
    c.trigger(); // a write lands while the first reload is still reading
    await vi.advanceTimersByTimeAsync(100);
    expect(refresh).toHaveBeenCalledTimes(1);
    release();
    await vi.advanceTimersByTimeAsync(100);
    expect(refresh).toHaveBeenCalledTimes(2);
    release();
    await vi.advanceTimersByTimeAsync(1000);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(most).toBe(1);
  });

  it("keeps going after a failed refresh, and does nothing once stopped", async () => {
    const refresh = vi.fn(async () => { throw new Error("offline"); });
    const c = coalesce(refresh, 10);
    c.trigger();
    await vi.advanceTimersByTimeAsync(10);
    c.trigger();
    await vi.advanceTimersByTimeAsync(10);
    expect(refresh).toHaveBeenCalledTimes(2);
    c.trigger();
    c.stop();
    await vi.advanceTimersByTimeAsync(100);
    expect(refresh).toHaveBeenCalledTimes(2);
  });
});
