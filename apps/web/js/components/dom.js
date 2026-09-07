/**
 * The three shared components are framework-free but not string-templated:
 * they build DOM. That is deliberate — they take user-typed identities and
 * resolved remote profiles, and building those into an HTML string is how an
 * injection lands. `el()` sets text and attributes through the DOM API, so
 * escaping is not something a caller can forget.
 *
 * They are also free of app-shell dependencies (no router, no toasts, no
 * storage), because EPIC-012 embeds them standalone in a public contact form.
 */

export function el(tag, props = {}, children = []) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (value === null || value === undefined || value === false) continue;
    if (key === "class") node.className = value;
    else if (key === "text") node.textContent = value;
    else if (key === "dataset") Object.assign(node.dataset, value);
    else if (key === "style") node.setAttribute("style", value);
    else if (key.startsWith("on") && typeof value === "function") {
      node.addEventListener(key.slice(2).toLowerCase(), value);
    } else node.setAttribute(key, value === true ? "" : String(value));
  }
  for (const child of [].concat(children)) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

export function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
  return node;
}

const AVATAR_PALETTE = [
  "#5856D6", "#FF2D55", "#FF9500", "#34C759",
  "#007AFF", "#AF52DE", "#FF3B30", "#00C7BE",
];

export function avatarColor(identity) {
  let hash = 0;
  for (const character of String(identity)) hash = (hash * 31 + character.charCodeAt(0)) >>> 0;
  return AVATAR_PALETTE[hash % AVATAR_PALETTE.length];
}

export const handleOf = (fqdn) => String(fqdn || "").split(".")[0] || "";
export const domainOf = (fqdn) => String(fqdn || "").split(".").slice(1).join(".");

/** An avatar: the resolved image when there is one, initials otherwise. */
export function avatar(identity, { size = "md", src = null } = {}) {
  const node = el("div", {
    class: `id-avatar ${size}`,
    style: `background:${avatarColor(identity)}`,
    text: (handleOf(identity)[0] || "?").toUpperCase(),
    "aria-hidden": "true",
  });
  if (src) {
    const image = el("img", { src, alt: "", class: "id-avatar-img", loading: "lazy" });
    // A broken or slow avatar falls back to the initials already rendered.
    image.addEventListener("load", () => node.append(image));
    image.addEventListener("error", () => {});
  }
  return node;
}
