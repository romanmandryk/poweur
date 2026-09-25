import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { startFaro } from "@poweur/faro";
import { readPage } from "./lib/page";
import { App } from "./App";

const page = readPage();
// Anonymous page views, errors and web vitals, same origin. The page is
// reported by name: paths carry transaction codes (/t/<txn>/…), and the
// scrubber removes any identity a page or error mentions.
if (page.telemetry) {
  startFaro({
    url: page.telemetry.url,
    app: "poweur-oauth",
    version: page.telemetry.version,
    pageUrl: () => `/${page.page}`,
  }).pageView(page.page);
}
document.title = page.title && page.title !== page.service.name ? `${page.title} · ${page.service.name}` : page.service.name;
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App page={page} />
  </StrictMode>,
);
