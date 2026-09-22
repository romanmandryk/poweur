import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./shell/App";
import { boot } from "./shell/boot";
import { installTestSeam } from "./shell/testSeam";
import { startObservability, screenName, syncIdentifiedUser } from "./lib/observability";
import { useRoute } from "./state/route";
import { useSession } from "./state/session";
import { ErrorBoundary } from "./shell/ErrorBoundary";

const root = document.getElementById("root");
if (!root) throw new Error("#root is missing from index.html");

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
  syncIdentifiedUser(lastId, lastUnlocked);
  useSession.subscribe((state) => {
    if (state.identity === lastId && state.unlocked === lastUnlocked) return;
    lastId = state.identity;
    lastUnlocked = state.unlocked;
    syncIdentifiedUser(lastId, lastUnlocked);
  });
});

// Decide the first screen before the first paint, as the legacy app did.
void boot();

createRoot(root).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
);
