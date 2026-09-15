import { useEffect } from "react";
import { DETAIL_SUBS, useRoute } from "../state/route";
import { closePanel, useUi } from "../state/ui";

interface CapacitorAppPlugin {
  addListener(event: "backButton", handler: () => void): Promise<{ remove(): void }> | { remove(): void };
}

/**
 * Back, without a URL history: the top panel closes first, then a sub-page
 * pops. Escape only pops a *detail* (thread, new chat) — a gate like unlock
 * has nowhere sensible to go back to. Android's hardware back does both.
 */
export function useBackNavigation() {
  useEffect(() => {
    const back = (): boolean => {
      const panel = useUi.getState().panel;
      if (panel) {
        closePanel(panel.id);
        return true;
      }
      const { sub, pop } = useRoute.getState();
      if (sub) {
        pop();
        return true;
      }
      return false;
    };

    const onKeydown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      // Radix owns Escape inside the panel and menus; inputs use it to close
      // their own suggestion lists.
      if (useUi.getState().panel) return;
      const target = event.target as HTMLElement | null;
      if (target?.closest?.("input, textarea, select, [role='menu']")) return;
      const { sub, pop } = useRoute.getState();
      if (sub && DETAIL_SUBS.has(sub)) pop();
    };
    document.addEventListener("keydown", onKeydown);

    const app = (globalThis as { Capacitor?: { Plugins?: { App?: CapacitorAppPlugin } } }).Capacitor?.Plugins?.App;
    const listener = app?.addListener("backButton", () => {
      back();
    });

    return () => {
      document.removeEventListener("keydown", onKeydown);
      void Promise.resolve(listener).then((handle) => handle?.remove());
    };
  }, []);
}
