import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./shell/App";
import { boot } from "./shell/boot";
import { installTestSeam } from "./shell/testSeam";
import { startObservability, screenName } from "./lib/observability";
import { useRoute } from "./state/route";
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
