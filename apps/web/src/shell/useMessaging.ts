import { useEffect } from "react";
import { loadInbox, pullAfterUnlock, stopEventStream } from "../actions/messages";
import { loadRequests } from "../actions/contacts";
import { useSession } from "../state/session";
import { submitPendingShareClaim } from "../actions/files";

/**
 * Messaging runs while an identity is unlocked, whichever screen is up: pull
 * everything waiting and hold the push stream; stop when it locks or switches.
 * The legacy app re-triggered loads on every render; here the stream, unlock
 * and returning to the tab are the triggers.
 */
export function useMessaging() {
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);

  useEffect(() => {
    if (!identity || !unlocked) return;
    pullAfterUnlock();
    void submitPendingShareClaim();
    // A backgrounded tab may have slept through its stream.
    const onVisible = () => {
      if (document.visibilityState !== "visible") return;
      void loadInbox({ force: true });
      void loadRequests();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
      stopEventStream();
    };
  }, [identity, unlocked]);
}
