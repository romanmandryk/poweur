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

  // Which calls to action get used. Only the kind of destination and the page it was on,
  // never the link itself and never anything typed (the claim box's name stays unrecorded).
  addEventListener("click", (e) => {
    const link = (e.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
    if (!link) return;
    let url: URL;
    try {
      url = new URL(link.href, location.href);
    } catch {
      return;
    }
    const target =
      url.hostname === "poweur.net" ? "app" :
      url.hostname === "github.com" ? "github" :
      url.hostname === location.hostname && url.pathname.startsWith("/docs") ? "docs" :
      url.hostname === location.hostname && url.pathname.startsWith("/legal") ? "legal" :
      "";
    if (target) t.event("cta_click", { target, from: location.pathname.replace(/index\.html$/, "") || "/" });
  });
  addEventListener("submit", (e) => {
    if ((e.target as Element | null)?.matches?.("form.claim")) t.event("claim_submit", { from: location.pathname || "/" });
  });
}

start();
