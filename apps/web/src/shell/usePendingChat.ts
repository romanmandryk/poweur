/**
 * Open the chat an identity page asked for (`?to=`, taken at boot) once this
 * identity's keys are open. The launcher only carries the target onward.
 */
import { useEffect } from "react";
import { openNewChat } from "../actions/messages";
import { clearPendingChatTarget, pendingChatTarget } from "../lib/visit";
import { useSession } from "../state/session";

export function usePendingChat() {
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);
  const hostMode = useSession((state) => state.mode.mode);
  // A host still being classified may turn out to be the launcher.
  const probed = useSession((state) => state.mode.probed || state.mode.resolved);

  useEffect(() => {
    if (!identity || !unlocked || !probed || hostMode === "launcher") return;
    const target = pendingChatTarget();
    if (!target) return;
    clearPendingChatTarget();
    if (target !== identity) void openNewChat(target);
  }, [identity, unlocked, probed, hostMode]);
}
