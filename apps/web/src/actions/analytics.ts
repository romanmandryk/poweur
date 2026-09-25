/**
 * Whether diagnostics may carry the Poweur ID (Settings → Diagnostics). It is
 * the relay's per-identity analytics consent (EPIC-013), stored in the
 * identity's home, so it can only be read once keys are open. Without it the
 * app's telemetry stays anonymous.
 */
import { setAnalyticsConsent } from "../lib/observability";
import { useSession } from "../state/session";
import { activeClient } from "./relay";

/** Read the unlocked identity's preference and apply it; off on any failure. */
export async function refreshAnalyticsConsent(): Promise<void> {
  const { identity, unlocked } = useSession.getState();
  const client: any = identity && unlocked ? activeClient() : null;
  if (!identity || !client) {
    setAnalyticsConsent(null, false);
    return;
  }
  let granted = false;
  try {
    granted = Boolean((await client.analyticsPreference())?.granted);
  } catch {
    granted = false;
  }
  // The identity may have locked or changed while the preference loaded.
  const now = useSession.getState();
  if (now.identity !== identity || !now.unlocked) return;
  setAnalyticsConsent(identity, granted);
}
