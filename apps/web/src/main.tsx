import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./shell/App";
import { isShellRuntime } from "./lib/storage.js";
import { boot, whenFirstRouteChosen } from "./shell/boot";
import { installTestSeam } from "./shell/testSeam";
import { refreshAnalyticsConsent } from "./actions/analytics";
import { startObservability, screenName, syncIdentifiedUser } from "./lib/observability";
import { DESTINATIONS, useRoute, type Destination } from "./state/route";
import { useSession } from "./state/session";
import { useData } from "./state/data";
import { ErrorBoundary } from "./shell/ErrorBoundary";
import { installPushClient, takeMovedNotice } from "./lib/push";
import { toast } from "./state/ui";
import { loadInbox } from "./actions/messages";
import { loadRequests } from "./actions/contacts";

const root = document.getElementById("root");
if (!root) throw new Error("#root is missing from index.html");
const mount = root;

installTestSeam();

void startObservability().then((obs) => {
  let last = screenName(useRoute.getState().page, useRoute.getState().sub);
  obs.pageChange(last);
  useRoute.subscribe((state) => {
    const name = screenName(state.page, state.sub);
    if (name === last) return;
    last = name;
    obs.pageChange(name);
  });
  let lastId = useSession.getState().identity;
  let lastUnlocked = useSession.getState().unlocked;
  // Nothing is sent until the unlocked identity has consented (its "Relay
  // analytics" preference); locking or switching stops it at once.
  const sync = () => {
    syncIdentifiedUser(lastId, lastUnlocked);
    if (lastId && lastUnlocked) void refreshAnalyticsConsent();
  };
  sync();
  useSession.subscribe((state) => {
    if (state.identity === lastId && state.unlocked === lastUnlocked) return;
    lastId = state.identity;
    lastUnlocked = state.unlocked;
    sync();
  });
});

// The app moved from /app/ to /id/ (EPIC-028); a home-screen install still
// points at the old address and has to be added again.
if (takeMovedNotice()) {
  toast("Poweur moved to a new address. If you added it to your home screen, remove it and add it again from Settings → Install app.", "info", 12_000);
}

// Notifications and install (E09-T12). A tap on a notification names a
// destination; a locked app goes there once it is unlocked.
installPushClient({
  onOpen: (page) => {
    // A sign-in prompt opens itself from the Messages list once the inbox is in.
    if (page === "signin") {
      useData.setState({ openNewestPrompt: true });
      page = "messages";
    }
    if (!(DESTINATIONS as readonly string[]).includes(page)) return;
    const go = () => useRoute.getState().go(page as Destination);
    if (useSession.getState().unlocked) {
      go();
      return;
    }
    const stop = useSession.subscribe((state) => {
      if (!state.unlocked) return;
      stop();
      go();
    });
  },
  onPush: () => {
    if (!useSession.getState().unlocked) return;
    void loadInbox({ force: true });
    void loadRequests();
  },
});

function render() {
  createRoot(mount).render(
    <StrictMode>
      <ErrorBoundary>
        <App />
      </ErrorBoundary>
    </StrictMode>,
  );
}

// Decide the first screen before the first paint, as the legacy app did.
// In the shell, wait until a poweur:// launch URL has been read so the
// approval screen is that first paint. Do not wait for the relay.
if (isShellRuntime()) {
  void boot();
  void whenFirstRouteChosen().then(render);
} else {
  void boot();
  render();
}
