/**
 * @vitest-environment happy-dom
 *
 * The join poller used to be `setInterval` with no in-flight guard. A
 * backgrounded phone waking up would fire every missed tick at once, consume
 * the seed on the first claim, 404 on the rest, and stack error toasts.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import {
  JOIN_POLL_MS, JOIN_TTL_MS,
  isTransientJoinError, describeJoinError, formatJoinRemaining, startJoinPoll,
} from "../js/enroll-wait.js";

const seed = new Uint8Array(32).fill(7);

beforeEach(() => { vi.useFakeTimers(); });
afterEach(() => { vi.useRealTimers(); });

describe("isTransientJoinError", () => {
  it("retries network and server failures", () => {
    expect(isTransientJoinError(new TypeError("Failed to fetch"))).toBe(true);
    expect(isTransientJoinError(Object.assign(new Error("aborted"), { name: "AbortError" }))).toBe(true);
    expect(isTransientJoinError({ status: 503, message: "unavailable" })).toBe(true);
    expect(isTransientJoinError({ status: 500 })).toBe(true);
    expect(isTransientJoinError({ status: 429, message: "slow down" })).toBe(true);
    expect(isTransientJoinError({ message: "network timeout" })).toBe(true);
  });

  it("stops on an expired or consumed rendezvous", () => {
    expect(isTransientJoinError({
      status: 404, relayCode: "not_found", message: "rendezvous not found or expired",
    })).toBe(false);
    expect(isTransientJoinError({ code: "decrypt_failed", message: "bad payload" })).toBe(false);
  });
});

describe("describeJoinError", () => {
  it("explains expiry instead of echoing the relay's 404", () => {
    expect(describeJoinError({
      status: 404, relayCode: "not_found", message: "rendezvous not found or expired",
    })).toMatch(/expired or was already used/i);
  });

  it("says so when the ten-minute window ran out", () => {
    expect(describeJoinError(new Error("x"), { timedOut: true })).toMatch(/10 minutes/i);
  });
});

describe("formatJoinRemaining", () => {
  it("renders a countdown and an expired state", () => {
    expect(formatJoinRemaining(125_000)).toBe("Expires in 2:05");
    expect(formatJoinRemaining(0)).toMatch(/expired/i);
  });
});

describe("startJoinPoll", () => {
  it("claims immediately rather than waiting a full interval", async () => {
    const claim = vi.fn(async () => null);
    const poller = startJoinPoll({ claim, onSeed: vi.fn(), onError: vi.fn() });
    await vi.advanceTimersByTimeAsync(0);
    expect(claim).toHaveBeenCalledTimes(1);
    poller.stop();
  });

  it("never overlaps claims, even when one takes longer than the interval", async () => {
    let inFlight = 0;
    let peak = 0;
    const claim = vi.fn(async () => {
      inFlight += 1;
      peak = Math.max(peak, inFlight);
      await new Promise((resolve) => setTimeout(resolve, JOIN_POLL_MS + 500));
      inFlight -= 1;
      return null;
    });
    const poller = startJoinPoll({ claim, onSeed: vi.fn(), onError: vi.fn() });
    await vi.advanceTimersByTimeAsync(JOIN_POLL_MS * 3);
    expect(peak).toBe(1);
    poller.stop();
  });

  it("reports a 404 once and does not keep claiming", async () => {
    const error = { status: 404, relayCode: "not_found", message: "rendezvous not found or expired" };
    const claim = vi.fn(async () => { throw error; });
    const onError = vi.fn();
    startJoinPoll({ claim, onSeed: vi.fn(), onError });
    await vi.advanceTimersByTimeAsync(0);
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0][1]).toEqual({ terminal: true });
    await vi.advanceTimersByTimeAsync(JOIN_POLL_MS * 4);
    expect(claim).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledTimes(1);
  });

  it("retries a network blip and still delivers the seed", async () => {
    const claim = vi.fn()
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(seed);
    const onError = vi.fn();
    const onSeed = vi.fn();
    startJoinPoll({ claim, onSeed, onError });
    await vi.advanceTimersByTimeAsync(0);
    expect(onError).toHaveBeenCalledWith(expect.any(TypeError), { terminal: false });
    expect(onSeed).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(JOIN_POLL_MS);
    expect(onSeed).toHaveBeenCalledTimes(1);
    expect(onSeed.mock.calls[0][0]).toBe(seed);
  });

  it("ignores a burst of wake-ups once a claim is in flight", async () => {
    let release;
    const claim = vi.fn(() => new Promise((resolve) => { release = () => resolve(null); }));
    const poller = startJoinPoll({ claim, onSeed: vi.fn(), onError: vi.fn() });
    await vi.advanceTimersByTimeAsync(0);
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("focus"));
    window.dispatchEvent(new Event("pageshow"));
    poller.checkNow();
    expect(claim).toHaveBeenCalledTimes(1);
    release();
    await vi.advanceTimersByTimeAsync(0);
    poller.stop();
  });

  it("polls again as soon as the tab becomes visible", async () => {
    const claim = vi.fn(async () => null);
    const poller = startJoinPoll({ claim, onSeed: vi.fn(), onError: vi.fn() });
    await vi.advanceTimersByTimeAsync(0);
    expect(claim).toHaveBeenCalledTimes(1);
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(0);
    expect(claim).toHaveBeenCalledTimes(2);
    poller.stop();
  });

  it("does not claim again after the seed arrives", async () => {
    const claim = vi.fn(async () => seed);
    const onSeed = vi.fn();
    const poller = startJoinPoll({ claim, onSeed, onError: vi.fn() });
    await vi.advanceTimersByTimeAsync(0);
    expect(onSeed).toHaveBeenCalledTimes(1);
    poller.checkNow();
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(JOIN_POLL_MS);
    expect(claim).toHaveBeenCalledTimes(1);
  });

  it("times out at the deadline without leaving a timer running", async () => {
    const claim = vi.fn(async () => null);
    const onError = vi.fn();
    startJoinPoll({
      claim, onSeed: vi.fn(), onError,
      deadlineMs: JOIN_POLL_MS,
      intervalMs: JOIN_POLL_MS,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(onError).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(JOIN_POLL_MS);
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0][0].code).toBe("join_timeout");
    expect(onError.mock.calls[0][1]).toEqual({ terminal: true });
    const calls = claim.mock.calls.length;
    await vi.advanceTimersByTimeAsync(JOIN_POLL_MS * 3);
    expect(claim).toHaveBeenCalledTimes(calls);
  });

  it("exposes the same period and TTL the CLI and relay use", () => {
    expect(JOIN_POLL_MS).toBe(2000);
    expect(JOIN_TTL_MS).toBe(10 * 60 * 1000);
  });
});
