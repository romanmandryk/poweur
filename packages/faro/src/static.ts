/**
 * The entry for poweur.org's static pages (the website and the Docusaurus
 * docs), which have no build of their own: `pnpm --filter @poweur/faro bundle`
 * turns this into one first-party script, served from the page's own origin.
 * Anonymous only: public pages have no identity to attach.
 */
import { startFaro } from "./index";

const LOCAL = /^(localhost|127\.0\.0\.1|\[::1\])$/;

function start() {
  if (LOCAL.test(location.hostname)) return;
  const docs = location.pathname.startsWith("/docs");
  const t = startFaro({ url: "/faro/collect", app: docs ? "poweur-docs" : "poweur-site" });
  const view = () => t.pageView(location.pathname.replace(/index\.html$/, "") || "/");
  view();
  // Docusaurus navigates without reloading: count those too.
  const push = history.pushState.bind(history);
  history.pushState = (...args: Parameters<History["pushState"]>) => {
    const before = location.pathname;
    push(...args);
    if (location.pathname !== before) view();
  };
  addEventListener("popstate", view);
}

start();
