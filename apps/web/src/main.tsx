import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./shell/App";
import { boot } from "./shell/boot";
import { installTestSeam } from "./shell/testSeam";

const root = document.getElementById("root");
if (!root) throw new Error("#root is missing from index.html");

installTestSeam();

// Decide the first screen before the first paint, as the legacy app did.
void boot();

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
