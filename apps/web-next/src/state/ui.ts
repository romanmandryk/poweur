/**
 * App-wide UI: theme, toasts, the loading overlay and the one open panel.
 * Callable from anywhere (`toast("Saved", "success")`), like the legacy
 * helpers, so async actions do not need a hook to report back.
 */
import type { ReactNode } from "react";
import { create } from "zustand";

export type Theme = "light" | "dark";
export type ToastType = "info" | "success" | "warning" | "error";

export interface ToastItem {
  id: number;
  key: string;
  message: string;
  type: ToastType;
}

export interface PanelItem {
  id: number;
  title: string;
  render: (close: () => void) => ReactNode;
  onClose?: () => void;
}

export interface UiState {
  theme: Theme;
  toasts: ToastItem[];
  loading: { active: boolean; text: string };
  panel: PanelItem | null;
}

const THEME_KEY = "poweur:theme";

function currentTheme(): Theme {
  return globalThis.document?.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

export const useUi = create<UiState>()(() => ({
  theme: currentTheme(),
  toasts: [],
  loading: { active: false, text: "Working…" },
  panel: null,
}));

let nextId = 1;

export function toast(message: string, type: ToastType = "info", duration = 3500) {
  // A backgrounded poller used to dump the same error a dozen times when the
  // phone woke up. Same message + kind already on screen stays one toast.
  const key = `${type}:${message}`;
  if (useUi.getState().toasts.some((item) => item.key === key)) return;
  const id = nextId++;
  useUi.setState((state) => ({ toasts: [...state.toasts, { id, key, message, type }] }));
  setTimeout(() => {
    useUi.setState((state) => ({ toasts: state.toasts.filter((item) => item.id !== id) }));
  }, duration + 400);
}

export function setLoading(active: boolean, text = "Working…") {
  useUi.setState({ loading: { active, text } });
}

/** Open the panel; returns the function that closes it. One panel at a time. */
export function openPanel(title: string, render: PanelItem["render"], onClose?: () => void): () => void {
  const id = nextId++;
  useUi.setState({ panel: { id, title, render, onClose } });
  return () => closePanel(id);
}

/** Close the open panel (or only panel `id`, so a stale close cannot shut a newer one). */
export function closePanel(id?: number) {
  const panel = useUi.getState().panel;
  if (!panel || (id !== undefined && panel.id !== id)) return;
  useUi.setState({ panel: null });
  panel.onClose?.();
}

export function setTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {
    // Private mode: the choice lasts this page load.
  }
  useUi.setState({ theme });
}

export function toggleTheme() {
  setTheme(useUi.getState().theme === "dark" ? "light" : "dark");
}
