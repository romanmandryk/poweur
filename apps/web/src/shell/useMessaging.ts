import { useEffect } from "react";
import { DriveClient } from "@poweur/client/drive";
import { loadHistory, loadInbox, pullAfterUnlock, stopEventStream } from "../actions/messages";
import { loadRequests } from "../actions/contacts";
import { clientFor, signerFor } from "../lib/client.js";
import { coalesce, touchesHistory } from "../lib/historySync";
import { useSession } from "../state/session";

const delay = (milliseconds: number, signal: AbortSignal) => new Promise<void>((resolve) => {
  const timer = window.setTimeout(resolve, milliseconds);
  signal.addEventListener("abort", () => { window.clearTimeout(timer); resolve(); }, { once: true });
});

/**
 * Messaging runs while an identity is unlocked, whichever screen is up: pull
 * everything waiting and hold the push stream; stop when it locks or switches.
 * The legacy app re-triggered loads on every render; here the stream, unlock
 * and returning to the tab are the triggers.
 *
 * Another device of this identity may pick a message up first, and then it
 * reaches this one only through the history on the drive: so a write to the
 * drive, or coming back to the tab, reloads the history too.
 */
export function useMessaging() {
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);

  useEffect(() => {
    if (!identity || !unlocked) return;
    pullAfterUnlock();
    const history = coalesce(() => loadHistory({ force: true }));
    // A backgrounded tab may have slept through its stream.
    const onVisible = () => {
      if (document.visibilityState !== "visible") return;
      void loadInbox({ force: true });
      void loadRequests();
      history.trigger();
    };
    document.addEventListener("visibilitychange", onVisible);
    const controller = new AbortController();
    void followDrive(identity, () => history.trigger(), controller.signal);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
      controller.abort();
      history.stop();
      stopEventStream();
    };
  }, [identity, unlocked]);
}

/** Hold this identity's drive stream, reconnecting with backoff, until aborted. */
async function followDrive(identity: string, onChange: () => void, signal: AbortSignal) {
  let retry = 500;
  let first = true;
  while (!signal.aborted) {
    const client = clientFor(identity);
    const keys = signerFor(identity);
    if (!client || !keys) return; // locked meanwhile; the next unlock starts again
    // Whatever was written while the stream was down announced itself to nobody.
    if (!first) onChange();
    first = false;
    try {
      await new DriveClient(client.relay, keys.signer, identity).subscribe((event) => {
        if (touchesHistory(event, identity)) onChange();
      }, signal);
      retry = 500;
    } catch {
      if (signal.aborted) break;
    }
    await delay(retry, signal);
    retry = Math.min(retry * 2, 15_000);
  }
}
