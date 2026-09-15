import { freshData, useData } from "../../src/state/data";
import { useRoute } from "../../src/state/route";
import { useSession } from "../../src/state/session";
import { useUi } from "../../src/state/ui";
import { resetBootForTests } from "../../src/shell/boot";

/** Stores are module singletons; every shell test starts from a blank device. */
export function resetStores() {
  localStorage.clear();
  sessionStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  resetBootForTests();
  useRoute.setState({ page: "messages", sub: null, params: {} });
  useData.setState(freshData());
  useSession.setState({ identity: null, config: {}, unlocked: false, mode: { mode: "unknown" } });
  useUi.setState({ theme: "light", toasts: [], panel: null, loading: { active: false, text: "Working…" } });
}
