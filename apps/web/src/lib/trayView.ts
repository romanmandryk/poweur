/**
 * The Inbox view mode (E09-T13) is a per-device reading preference, kept per
 * identity in localStorage — not account state. Default: By ID.
 */
import type { TrayView } from "../state/data";

const key = (identity: string) => `poweur:tray-view:${identity.toLowerCase()}`;

export function loadTrayView(identity: string): TrayView {
  try {
    return localStorage.getItem(key(identity)) === "subject" ? "subject" : "id";
  } catch {
    return "id";
  }
}

export function saveTrayView(identity: string, view: TrayView) {
  try {
    localStorage.setItem(key(identity), view);
  } catch {
    // Private mode: the choice lasts the session.
  }
}
