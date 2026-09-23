/**
 * In-memory routing, mirroring the legacy `R` object: the app has no URL
 * routes (deep links stay `#claim=` / `?auth=` / `poweur://auth`), so neither does this.
 */
import { create } from "zustand";
import { useData } from "./data";

/** The five primary destinations, in nav order. */
export const DESTINATIONS = ["messages", "contacts", "files", "launcher", "settings"] as const;
export type Destination = (typeof DESTINATIONS)[number];

export type SubPageId = "add-id" | "unlock" | "new-chat" | "thread" | "onboarding" | "claim" | "auth" | "pair";

/**
 * Sub-pages that are a *detail of the list behind them* rather than a gate
 * (E15-T11): full-screen on a phone, beside the list on a wide screen. Gates
 * are deliberately not here — an inbox behind an unlock prompt would suggest
 * it is reachable.
 */
export const DETAIL_SUBS: ReadonlySet<SubPageId> = new Set<SubPageId>(["new-chat", "thread"]);

export interface RouteState {
  page: Destination;
  sub: SubPageId | null;
  params: Record<string, unknown>;
  go(page: Destination, params?: Record<string, unknown>): void;
  push(sub: SubPageId, params?: Record<string, unknown>): void;
  pop(): void;
}

export const useRoute = create<RouteState>()((set, get) => ({
  page: "messages",
  sub: null,
  params: {},
  go: (page, params = {}) => set({ page, sub: null, params }),
  push: (sub, params = {}) => set({ sub, params }),
  pop: () => {
    if (get().sub === "thread") useData.setState({ thread: null });
    set({ sub: null, params: {} });
  },
}));
