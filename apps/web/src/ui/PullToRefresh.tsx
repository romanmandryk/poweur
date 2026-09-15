/**
 * Pull down at the top of a list to read it again (Messages, Contacts). The
 * content follows the finger, a spinner appears in the gap it leaves, and it
 * stays there until the refresh settles. Touch only: a mouse has other ways to
 * ask, and a desktop drag should keep selecting text.
 */
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { RefreshCw } from "lucide-react";
import { cn } from "../lib/cn";
import { setRefreshing } from "../state/ui";

/** How far the content has to travel, after resistance, to count as a pull. */
export const PULL_THRESHOLD = 64;
const PULL_MAX = 110;
/** Where the content rests while the refresh runs. */
const HOLD = 56;
/** The finger moves twice as far as the content: a pull, not a drag. */
const RESISTANCE = 0.5;

export function PullToRefresh({
  onRefresh,
  children,
  className,
}: {
  onRefresh: () => Promise<unknown> | unknown;
  children: ReactNode;
  className?: string;
}) {
  const root = useRef<HTMLDivElement>(null);
  const handler = useRef(onRefresh);
  const [pull, setPull] = useState(0);
  const [dragging, setDragging] = useState(false);
  const [refreshing, setBusy] = useState(false);

  useLayoutEffect(() => {
    handler.current = onRefresh;
  });

  useEffect(() => {
    const element = root.current;
    if (!element) return;
    // The shell's scrolling main, or whatever this list scrolls inside.
    const scroller = element.closest<HTMLElement>("#page-content") ?? element.parentElement ?? element;
    let startY: number | null = null;
    let distance = 0;
    let busy = false;
    let live = true;

    const move = (value: number) => {
      distance = value;
      setPull(value);
    };

    const onStart = (event: TouchEvent) => {
      startY = !busy && event.touches.length === 1 && scroller.scrollTop <= 0 ? event.touches[0].clientY : null;
    };

    const onMove = (event: TouchEvent) => {
      if (startY === null || !event.touches.length) return;
      const dy = event.touches[0].clientY - startY;
      if (dy <= 0 || scroller.scrollTop > 0) {
        if (distance) move(0);
        setDragging(false);
        return;
      }
      // The gesture is ours now: without this the list rubber-bands instead of
      // the content following the finger.
      if (event.cancelable) event.preventDefault();
      setDragging(true);
      move(Math.min(PULL_MAX, dy * RESISTANCE));
    };

    const onEnd = () => {
      if (startY === null) return;
      startY = null;
      setDragging(false);
      if (distance < PULL_THRESHOLD) {
        move(0);
        return;
      }
      busy = true;
      setBusy(true);
      setRefreshing(true);
      move(HOLD);
      Promise.resolve()
        .then(() => handler.current())
        .catch(() => {})
        .finally(() => {
          busy = false;
          setRefreshing(false);
          if (!live) return;
          setBusy(false);
          move(0);
        });
    };

    element.addEventListener("touchstart", onStart, { passive: true });
    element.addEventListener("touchmove", onMove, { passive: false });
    element.addEventListener("touchend", onEnd);
    element.addEventListener("touchcancel", onEnd);
    return () => {
      live = false;
      element.removeEventListener("touchstart", onStart);
      element.removeEventListener("touchmove", onMove);
      element.removeEventListener("touchend", onEnd);
      element.removeEventListener("touchcancel", onEnd);
    };
  }, []);

  const transition = dragging ? "none" : "transform 200ms ease, height 200ms ease";

  return (
    <div ref={root} className={cn("pull-to-refresh relative", className)}>
      <div
        aria-hidden={!refreshing}
        className="ptr-indicator pointer-events-none absolute inset-x-0 top-0 flex items-end justify-center overflow-hidden"
        style={{ height: pull, transition }}
      >
        <span
          role={refreshing ? "status" : undefined}
          aria-label={refreshing ? "Refreshing" : undefined}
          className="mb-2.5 flex size-9 shrink-0 items-center justify-center rounded-full bg-surface shadow-pop"
          style={{ opacity: refreshing ? 1 : Math.min(1, pull / PULL_THRESHOLD) }}
        >
          <RefreshCw
            className={cn("size-[18px] text-accent", refreshing && "animate-spin")}
            style={refreshing ? undefined : { transform: `rotate(${pull * 3}deg)` }}
            aria-hidden="true"
          />
        </span>
      </div>
      {/* No transform at rest: a transformed ancestor would capture fixed-position children. */}
      <div className="ptr-content" style={pull ? { transform: `translateY(${pull}px)`, transition } : { transition }}>
        {children}
      </div>
    </div>
  );
}
