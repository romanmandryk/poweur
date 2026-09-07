/**
 * IdentityInput — type or search a Poweur ID.
 *
 * The single entry point for "who?" across the app: compose, add-contact and
 * the share audience picker all use it, so validation, contact autocomplete
 * and the resolved preview are written once. It answers with a *resolved*
 * identity, not a string, which is what stops "send to a typo" from being a
 * silent failure three screens later.
 *
 * Deliberately free of app-shell imports — EPIC-012 embeds it in a public
 * contact form with no router, no toasts and no local storage.
 */

import { avatar, clear, el, handleOf } from "./dom.js";
import { ProfileCard } from "./profile-card.js";

/** The relay's own rule, mirrored client-side so typos surface before a request. */
const IDENTITY_RE = /^(?=.{3,253}$)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/;

export function isValidIdentity(value) {
  return IDENTITY_RE.test(String(value || "").trim().toLowerCase());
}

/**
 * @param {object} options
 * @param {(identity: string) => Promise<object>} options.resolve
 * @param {Array<{identity: string, petname?: string}>} [options.contacts]  autocomplete source
 * @param {string} [options.value]
 * @param {string} [options.placeholder]
 * @param {string} [options.label]
 * @param {boolean} [options.preview]        show a ProfileCard for the resolved ID
 * @param {(result: {identity: string, entry: object}|null) => void} [options.onChange]
 * @param {(identity: string) => void} [options.onSubmit]
 */
export function IdentityInput({
  resolve,
  contacts = [],
  value = "",
  placeholder = "alice.poweur.net",
  label = null,
  preview = true,
  onChange = () => {},
  onSubmit = null,
} = {}) {
  const inputId = `idin-${Math.random().toString(36).slice(2, 9)}`;
  const input = el("input", {
    id: inputId,
    class: "input",
    type: "text",
    value,
    placeholder,
    autocomplete: "off",
    spellcheck: "false",
    inputmode: "url",
    role: "combobox",
    "aria-expanded": "false",
    "aria-autocomplete": "list",
  });

  const suggestions = el("ul", { class: "idin-suggestions", role: "listbox", hidden: true });
  const status = el("p", { class: "idin-status small", role: "status", "aria-live": "polite" });
  const previewSlot = el("div", { class: "idin-preview" });

  const root = el("div", { class: "idin" }, [
    label && el("label", { class: "form-label", for: inputId, text: label }),
    input,
    suggestions,
    status,
    preview && previewSlot,
  ]);

  let resolved = null;      // { identity, entry } once a lookup succeeds
  let resolveToken = 0;     // guards against an earlier lookup landing last
  let highlighted = -1;

  const current = () => input.value.trim().toLowerCase();

  function matches() {
    const query = current();
    if (!query) return [];
    return contacts
      .filter((contact) => {
        const haystack = `${contact.identity} ${contact.petname ?? ""}`.toLowerCase();
        return haystack.includes(query) && contact.identity.toLowerCase() !== query;
      })
      .slice(0, 6);
  }

  function renderSuggestions() {
    const list = matches();
    clear(suggestions);
    highlighted = -1;
    suggestions.hidden = list.length === 0;
    input.setAttribute("aria-expanded", String(list.length > 0));
    list.forEach((contact, index) => {
      suggestions.append(el("li", {
        class: "idin-suggestion",
        role: "option",
        id: `${inputId}-opt-${index}`,
        dataset: { identity: contact.identity },
        // mousedown, not click: blur would close the list before click lands.
        onMousedown: (event) => { event.preventDefault(); choose(contact.identity); },
      }, [
        avatar(contact.identity, { size: "md" }),
        el("span", { class: "idin-suggestion-name", text: contact.petname || handleOf(contact.identity) }),
        el("span", { class: "idin-suggestion-id small muted", text: contact.identity }),
      ]));
    });
  }

  function highlight(delta) {
    const items = [...suggestions.children];
    if (!items.length) return;
    items[highlighted]?.classList.remove("active");
    highlighted = (highlighted + delta + items.length) % items.length;
    items[highlighted].classList.add("active");
    input.setAttribute("aria-activedescendant", items[highlighted].id);
  }

  function choose(identity) {
    input.value = identity;
    suggestions.hidden = true;
    lookup();
  }

  function setStatus(text, tone = "") {
    status.textContent = text;
    status.className = `idin-status small ${tone}`;
  }

  async function lookup() {
    const identity = current();
    clear(previewSlot);
    resolved = null;

    if (!identity) { setStatus(""); onChange(null); return null; }
    if (!isValidIdentity(identity)) {
      setStatus("That does not look like a Poweur ID (try alice.poweur.net)", "val-warn");
      onChange(null);
      return null;
    }

    const token = ++resolveToken;
    setStatus("Looking up…");
    try {
      const entry = await resolve(identity);
      if (token !== resolveToken) return null; // a newer lookup won
      resolved = { identity, entry };
      setStatus("Found", "val-ok");
      if (preview) previewSlot.append(ProfileCard({ identity, resolve, cached: entry, compact: true }).el);
      onChange(resolved);
      return resolved;
    } catch (error) {
      if (token !== resolveToken) return null;
      setStatus(`Not found: ${error.message}`, "val-warn");
      onChange(null);
      return null;
    }
  }

  let debounce;
  input.addEventListener("input", () => {
    renderSuggestions();
    setStatus("");
    clearTimeout(debounce);
    debounce = setTimeout(lookup, 400);
  });
  input.addEventListener("blur", () => setTimeout(() => { suggestions.hidden = true; }, 120));
  input.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown") { event.preventDefault(); highlight(1); }
    else if (event.key === "ArrowUp") { event.preventDefault(); highlight(-1); }
    else if (event.key === "Escape") { suggestions.hidden = true; }
    else if (event.key === "Enter") {
      event.preventDefault();
      const active = suggestions.children[highlighted];
      if (active && !suggestions.hidden) { choose(active.dataset.identity); return; }
      clearTimeout(debounce);
      lookup().then((result) => { if (result && onSubmit) onSubmit(result.identity); });
    }
  });

  if (value) lookup();

  return {
    el: root,
    input,
    /** The resolved identity, or null when empty/invalid/unresolvable. */
    value: () => resolved,
    /** The raw text, whether or not it resolved — compose still lets you try. */
    raw: current,
    setValue(next) { input.value = next ?? ""; return lookup(); },
    focus: () => input.focus(),
    /** Force a lookup now (Enter, or a caller about to submit). */
    lookup,
  };
}
