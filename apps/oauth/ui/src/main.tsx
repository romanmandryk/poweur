import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { readPage } from "./lib/page";
import { App } from "./App";

const page = readPage();
document.title = page.title && page.title !== page.service.name ? `${page.title} · ${page.service.name}` : page.service.name;
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App page={page} />
  </StrictMode>,
);
