/** Keep an opened Files drive current without making navigation wait for it. */
import { useEffect } from "react";
import { refreshBrowserFiles } from "../actions/files";
import { useData } from "../state/data";
import { useSession } from "../state/session";

const delay = (milliseconds: number, signal: AbortSignal) => new Promise<void>((resolve) => {
  const timer = window.setTimeout(resolve, milliseconds);
  signal.addEventListener("abort", () => { window.clearTimeout(timer); resolve(); }, { once: true });
});

export function useFilesSync() {
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);
  const drive = useData((state) => state.files.own?.files.client);

  useEffect(() => {
    if (!identity || !unlocked || !drive || typeof drive.subscribe !== "function") return;
    const controller = new AbortController();
    let refreshing: Promise<void> | null = null;
    const refresh = () => {
      refreshing ??= refreshBrowserFiles(identity).catch(() => {}).finally(() => { refreshing = null; });
      return refreshing;
    };
    const onVisible = () => { if (document.visibilityState === "visible") void refresh(); };
    document.addEventListener("visibilitychange", onVisible);
    void (async () => {
      let retry = 500;
      while (!controller.signal.aborted) {
        try {
          await drive.subscribe(async (event) => {
            if (event.type !== "drive.changed" || event.drive?.drive !== identity) return;
            await refresh();
          }, controller.signal);
          retry = 500;
        } catch {
          if (controller.signal.aborted) break;
        }
        await delay(retry, controller.signal);
        retry = Math.min(retry * 2, 15_000);
      }
    })();
    return () => { document.removeEventListener("visibilitychange", onVisible); controller.abort(); };
  }, [drive, identity, unlocked]);
}
