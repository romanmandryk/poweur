/**
 * Keep the message history current across this identity's devices.
 *
 * The relay holds one inbox per identity, and whichever device picks a message
 * up first takes it: the others only see it once it is in the history on the
 * drive. The drive announces each write (`drive.changed`), so a device reloads
 * its history when one arrives instead of waiting to be reopened.
 */

/** Advisory drive events, as `DriveClient.subscribe` hands them over. */
export interface DriveEvent {
  type: string;
  drive?: { drive: string; operation?: string };
}

/**
 * Whether an event can have changed this identity's history or read marks:
 * any write to its own drive. History logs are appended and the read marks
 * replaced, and the event does not say which file it was, so both count.
 */
export function touchesHistory(event: DriveEvent, identity: string): boolean {
  if (event.type !== "drive.changed" || !event.drive) return false;
  return String(event.drive.drive).toLowerCase() === identity.toLowerCase();
}

/**
 * Runs `refresh` after `delayMs` of quiet, once per burst of triggers. A
 * trigger while a refresh is running schedules one more after it, so the last
 * write of a burst is never missed and refreshes never overlap.
 */
export function coalesce(refresh: () => Promise<unknown>, delayMs = 400) {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let running = false;
  let again = false;
  let stopped = false;

  const run = async () => {
    timer = null;
    if (stopped) return;
    if (running) {
      again = true;
      return;
    }
    running = true;
    try {
      await refresh();
    } catch {
      // The next write, unlock or pull-to-refresh tries again.
    } finally {
      running = false;
    }
    if (again && !stopped) {
      again = false;
      schedule();
    }
  };
  const schedule = () => {
    if (stopped) return;
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => void run(), delayMs);
  };
  return {
    trigger: schedule,
    stop() {
      stopped = true;
      if (timer !== null) clearTimeout(timer);
      timer = null;
    },
  };
}
