import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { ActivityIndicator, destinationBusy } from "../../src/shell/ActivityIndicator";
import { useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { useUi } from "../../src/state/ui";
import { resetStores } from "../helpers/stores";

beforeEach(resetStores);
afterEach(() => {
  vi.useRealTimers();
  useUi.setState({ refreshing: false });
});

describe("destinationBusy", () => {
  it("counts Contacts and Files while they load", () => {
    const data = useData.getState();
    expect(destinationBusy("contacts", { ...data, contacts: { ...data.contacts, loading: true } })).toBe(true);
    expect(destinationBusy("files", { ...data, files: { ...data.files, loading: true } })).toBe(true);
    expect(destinationBusy("contacts", data)).toBe(false);
    expect(destinationBusy("settings", { ...data, contacts: { ...data.contacts, loading: true } })).toBe(false);
  });

  it("counts a first load on Messages, never a background drain", () => {
    const data = useData.getState();
    expect(destinationBusy("messages", { ...data, history: { ...data.history, loading: true } })).toBe(true);
    expect(destinationBusy("messages", { ...data, history: { ...data.history, loading: true, loaded: true } })).toBe(false);
    expect(destinationBusy("messages", { ...data, tray: "requests", requests: { ...data.requests, loading: true } })).toBe(true);
    // The requests queue drains on every push; on the inbox tray that is silent.
    expect(destinationBusy("messages", { ...data, requests: { ...data.requests, loading: true } })).toBe(false);
  });
});

describe("ActivityIndicator", () => {
  const indicator = () => document.querySelector('.activity-indicator[role="status"]');

  it("appears only after a moment, and gives way to a pull-to-refresh spinner", () => {
    vi.useFakeTimers();
    useRoute.setState({ page: "contacts" });
    render(<ActivityIndicator />);

    act(() => useData.setState((state) => ({ contacts: { ...state.contacts, loading: true } })));
    expect(indicator()).toBeNull();
    act(() => vi.advanceTimersByTime(300));
    expect(indicator()).toBeTruthy();

    act(() => useUi.setState({ refreshing: true }));
    expect(indicator()).toBeNull();

    act(() => {
      useUi.setState({ refreshing: false });
      useData.setState((state) => ({ contacts: { ...state.contacts, loading: false } }));
    });
    act(() => vi.advanceTimersByTime(300));
    expect(indicator()).toBeNull();
  });

  it("a read faster than the delay never shows it", () => {
    vi.useFakeTimers();
    useRoute.setState({ page: "contacts" });
    render(<ActivityIndicator />);
    act(() => useData.setState((state) => ({ contacts: { ...state.contacts, loading: true } })));
    act(() => vi.advanceTimersByTime(100));
    act(() => useData.setState((state) => ({ contacts: { ...state.contacts, loading: false } })));
    act(() => vi.advanceTimersByTime(500));
    expect(indicator()).toBeNull();
  });
});
