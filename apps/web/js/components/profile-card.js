/**
 * ProfileCard — who an identity is, with the right action for the context.
 *
 * Used wherever a person appears: a contact row, a message sender, a share
 * grantee, and (EPIC-012) a public contact form. It resolves asynchronously
 * and renders three states in order — skeleton, resolved, unresolvable —
 * because a card that blocks its container until DNS answers is worse than
 * one that fills in.
 */

import { avatar, clear, domainOf, el, handleOf } from "./dom.js";

/**
 * @param {object} options
 * @param {string} options.identity
 * @param {(identity: string) => Promise<object>} options.resolve  usually `resolveProfile`
 * @param {object|null} [options.cached]      a synchronous first paint, if available
 * @param {{label: string, onSelect: Function}|null} [options.action]  message / add contact / accept
 * @param {boolean} [options.compact]         row form rather than full card
 */
export function ProfileCard({ identity, resolve, cached = null, action = null, compact = false }) {
  const root = el("div", {
    class: `profile-card${compact ? " compact" : ""}`,
    dataset: { identity },
  });

  const render = (state, entry) => {
    clear(root);
    root.dataset.state = state;

    root.append(avatar(identity, { size: compact ? "md" : "lg", src: entry?.avatar ?? null }));

    const name = entry?.displayName || handleOf(identity);
    const body = el("div", { class: "profile-card-body" }, [
      el("div", { class: "profile-card-name", text: name }),
      el("div", { class: "profile-card-id", text: identity, title: identity }),
    ]);

    if (state === "loading") {
      body.append(el("div", { class: "profile-card-meta muted small", text: "Resolving…" }));
    } else if (state === "error") {
      body.append(el("div", { class: "profile-card-meta small val-warn", text: "Could not resolve this identity" }));
    } else {
      if (!compact && entry.bio) {
        body.append(el("div", { class: "profile-card-bio", text: entry.bio }));
      }
      const features = Object.keys(entry.capabilities?.features ?? {});
      if (!compact && features.length) {
        body.append(el("div", { class: "profile-card-caps" },
          features.map((feature) => el("span", { class: "chip", text: feature }))));
      }
      if (!compact) {
        for (const link of entry.links.slice(0, 4)) {
          if (!/^https?:\/\//i.test(link.url ?? "")) continue; // never render javascript:
          body.append(el("a", {
            class: "profile-card-link small",
            href: link.url,
            target: "_blank",
            rel: "noopener noreferrer nofollow",
            text: link.label || link.url,
          }));
        }
      }
    }
    root.append(body);

    if (state !== "loading") {
      root.append(el("div", { class: "profile-card-domain small muted", text: domainOf(identity) }));
    }
    if (action) {
      root.append(el("button", {
        class: "btn btn-sm btn-primary profile-card-action",
        text: action.label,
        disabled: state === "error" && action.requiresResolution !== false,
        onClick: (event) => { event.stopPropagation(); action.onSelect(identity, entry); },
      }));
    }
  };

  render(cached ? "ready" : "loading", cached);

  const ready = Promise.resolve(cached ?? resolve(identity))
    .then((entry) => { render("ready", entry); return entry; })
    .catch((error) => { render("error", null); return null; });

  return { el: root, ready };
}
