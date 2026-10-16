import { useEffect } from "react";
import { updateAvailable } from "../lib/update";
import { useSession } from "../state/session";
import { useUi } from "../state/ui";

/** At most one look per this long; coming back to the app is the trigger. */
const MIN_INTERVAL = 5 * 60 * 1000;

/**
 * Look for a newer build when the app opens and whenever it comes back to
 * the foreground (a notification tap focuses a page that may be hours old).
 * Locked, nothing is lost by reloading at once; unlocked, a banner offers it,
 * so a draft or an unlock is never thrown away without asking.
 */
export function useUpdateCheck() {
  useEffect(() => {
    let last = 0;
    const check = async () => {
      if (document.visibilityState !== "visible" || Date.now() - last < MIN_INTERVAL || useUi.getState().updateReady) return;
      last = Date.now();
      if (!(await updateAvailable())) return;
      if (!useSession.getState().unlocked) window.location.reload();
      else useUi.setState({ updateReady: true });
    };
    void check();
    document.addEventListener("visibilitychange", check);
    return () => document.removeEventListener("visibilitychange", check);
  }, []);
}
