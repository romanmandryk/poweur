import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { PULL_THRESHOLD, PullToRefresh } from "../../src/ui/PullToRefresh";
import { useUi } from "../../src/state/ui";

afterEach(() => useUi.setState({ refreshing: false }));

function touch(target: Element, type: string, clientY?: number) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "touches", { value: clientY === undefined ? [] : [{ clientY }] });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

function setup(onRefresh: () => unknown, scrollTop = 0) {
  const { container } = render(
    <main id="page-content">
      <PullToRefresh onRefresh={onRefresh}>
        <p id="row">row</p>
      </PullToRefresh>
    </main>,
  );
  Object.defineProperty(container.querySelector("#page-content")!, "scrollTop", { value: scrollTop, configurable: true });
  return container.querySelector("#row")!;
}

/** A finger pulled `by` pixels; the content travels half of that. */
function pull(row: Element, by: number) {
  touch(row, "touchstart", 100);
  const move = touch(row, "touchmove", 100 + by);
  touch(row, "touchend");
  return move;
}

const spinner = () => document.querySelector('[role="status"][aria-label="Refreshing"]');

describe("PullToRefresh", () => {
  it("a long enough pull at the top refreshes, with a spinner until it settles", async () => {
    let finish!: () => void;
    const onRefresh = vi.fn(() => new Promise<void>((resolve) => (finish = resolve)));
    const row = setup(onRefresh);

    const move = pull(row, PULL_THRESHOLD * 2 + 20);
    expect(move.defaultPrevented).toBe(true);
    await act(async () => {});

    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(spinner()).toBeTruthy();
    expect(useUi.getState().refreshing).toBe(true);

    await act(async () => finish());
    expect(spinner()).toBeNull();
    expect(useUi.getState().refreshing).toBe(false);
  });

  it("a short pull springs back without refreshing", async () => {
    const onRefresh = vi.fn();
    const row = setup(onRefresh);
    pull(row, PULL_THRESHOLD);
    await act(async () => {});
    expect(onRefresh).not.toHaveBeenCalled();
    expect(spinner()).toBeNull();
  });

  it("leaves a scrolled list to scroll", async () => {
    const onRefresh = vi.fn();
    const row = setup(onRefresh, 120);
    const move = pull(row, 300);
    await act(async () => {});
    expect(move.defaultPrevented).toBe(false);
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("a failed refresh still puts the spinner away", async () => {
    const onRefresh = vi.fn(() => Promise.reject(new Error("offline")));
    const row = setup(onRefresh);
    pull(row, 300);
    await act(async () => {});
    await act(async () => {});
    expect(onRefresh).toHaveBeenCalledTimes(1);
    expect(spinner()).toBeNull();
    expect(useUi.getState().refreshing).toBe(false);
  });
});
