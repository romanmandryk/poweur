/**
 * New-device join poller (E11-T3 ceremony, web half).
 *
 * The relay holds a rendezvous for ten minutes and the new device must claim
 * it. That used to be `setInterval(..., 2000)`. A phone that backgrounds
 * while the other device types the code comes back with a burst of missed
 * timer firings: the first claim consumes the seed, the rest 404, and the
 * user sees a stack of identical error toasts. One in-flight claim, a
 * timeout-after-completion loop, and a visibility wake fix that.
 *
 * Matches the CLI `--wait` contract: 2 s between attempts, 10 min deadline
 * (`apps/cli/internal/cli/enroll.go`, `apps/api/internal/relay/enroll.go`).
 */

/** Time between claim attempts while the other device has not approved. */
export const JOIN_POLL_MS = 2000;

/** Client-side cap; the relay TTL is the same. */
export const JOIN_TTL_MS = 10 * 60 * 1000;

/**
 * Network blips and 5xx should keep waiting. A 404 means the rendezvous is
 * gone (expired, cancelled, or already claimed) and retrying will not help.
 */
export function isTransientJoinError(error) {
  if (!error || typeof error !== "object") return true;
  const name = error.name ?? "";
  if (name === "AbortError" || name === "TimeoutError") return true;
  if (error instanceof TypeError) return true;
  const status = Number(error.status);
  if (!Number.isFinite(status) || status === 0) {
    const message = String(error.message ?? "");
    return /fetch|network|timeout|abort|offline|failed to fetch/i.test(message)
      || name === "TypeError";
  }
  if (status >= 500 && status <= 599) return true;
  if (status === 408 || status === 429) return true;
  return false;
}

/** One sentence the waiting panel and a single toast can share. */
export function describeJoinError(error, { timedOut = false } = {}) {
  if (timedOut || error?.code === "join_timeout") {
    return "No approval within 10 minutes. Tap Try again for a new code.";
  }
  const status = Number(error?.status);
  const code = error?.relayCode ?? error?.code ?? "";
  const raw = String(error?.message ?? "");
  if (status === 404 || code === "not_found") {
    return "This request expired or was already used. Tap Try again for a new code.";
  }
  if (code === "decrypt_failed") {
    return "The keys arrived damaged. Tap Try again for a new code.";
  }
  if (isTransientJoinError(error)) {
    return raw
      ? `Couldn't reach the relay (${raw}). Retrying…`
      : "Couldn't reach the relay. Retrying…";
  }
  return raw || "The other device could not finish approving.";
}

export function formatJoinRemaining(ms) {
  if (ms <= 0) return "This request has expired.";
  const total = Math.ceil(ms / 1000);
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `Expires in ${minutes}:${String(seconds).padStart(2, "0")}`;
}

/**
 * @param {object} options
 * @param {() => Promise<Uint8Array | null>} options.claim
 * @param {(seed: Uint8Array) => void | Promise<void>} options.onSeed
 * @param {(error: unknown, info: { terminal: boolean }) => void} options.onError
 * @param {(text: string) => void} [options.onTick]
 * @param {() => number} [options.now]
 * @param {number} [options.intervalMs]
 * @param {number} [options.deadlineMs]
 * @param {Pick<Document, "addEventListener" | "removeEventListener" | "visibilityState">} [options.documentRef]
 * @param {Pick<Window, "addEventListener" | "removeEventListener">} [options.windowRef]
 */
export function startJoinPoll({
  claim,
  onSeed,
  onError,
  onTick,
  now = () => Date.now(),
  intervalMs = JOIN_POLL_MS,
  deadlineMs = JOIN_TTL_MS,
  documentRef = globalThis.document,
  windowRef = globalThis.window,
}) {
  let stopped = false;
  let inFlight = false;
  let timer = null;
  let tickTimer = null;
  const startedAt = now();

  const remaining = () => deadlineMs - (now() - startedAt);

  const announce = () => {
    onTick?.(formatJoinRemaining(remaining()));
  };

  const stop = () => {
    stopped = true;
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
    if (tickTimer !== null) {
      clearInterval(tickTimer);
      tickTimer = null;
    }
    documentRef?.removeEventListener?.("visibilitychange", onVisible);
    windowRef?.removeEventListener?.("focus", onVisible);
    windowRef?.removeEventListener?.("pageshow", onVisible);
  };

  const finishError = (error, terminal) => {
    if (stopped && terminal) return;
    if (terminal) stop();
    onError(error, { terminal });
  };

  const schedule = (delay = intervalMs) => {
    if (stopped) return;
    timer = setTimeout(() => {
      timer = null;
      tick();
    }, delay);
  };

  const tick = async () => {
    if (stopped || inFlight) return;
    if (remaining() <= 0) {
      const timeout = new Error("timed out waiting for approval");
      timeout.code = "join_timeout";
      finishError(timeout, true);
      return;
    }
    inFlight = true;
    let seed;
    try {
      seed = await claim();
    } catch (error) {
      if (!stopped) {
        const terminal = !isTransientJoinError(error);
        finishError(error, terminal);
        if (!terminal) schedule();
      }
      return;
    } finally {
      inFlight = false;
    }
    if (stopped) return;
    if (seed) {
      stop();
      await onSeed(seed);
      return;
    }
    announce();
    schedule();
  };

  function onVisible() {
    if (stopped) return;
    if (documentRef && documentRef.visibilityState && documentRef.visibilityState !== "visible") {
      return;
    }
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
    tick();
  }

  documentRef?.addEventListener?.("visibilitychange", onVisible);
  windowRef?.addEventListener?.("focus", onVisible);
  windowRef?.addEventListener?.("pageshow", onVisible);

  announce();
  tickTimer = setInterval(announce, 1000);
  tick();

  return {
    stop,
    checkNow: () => {
      if (stopped || inFlight) return;
      if (timer !== null) {
        clearTimeout(timer);
        timer = null;
      }
      tick();
    },
  };
}
