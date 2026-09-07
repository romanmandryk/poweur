/**
 * AudiencePicker — "who is this for?" as a multi-select over contacts, local
 * groups and free-typed identities.
 *
 * The share dialog (E15-T4) is the first consumer and the group editor is the
 * next. Owner-local groups only: addressable *group identities* are EPIC-005
 * T5 and would change what a selection means, so the shape here is a flat list
 * of identities plus the group each one came from.
 */

import { avatar, clear, el, handleOf } from "./dom.js";
import { IdentityInput } from "./identity-input.js";

/**
 * @param {object} options
 * @param {(identity: string) => Promise<object>} options.resolve
 * @param {Array<{identity: string, petname?: string}>} [options.contacts]
 * @param {Array<{id: string, name: string, members: string[]}>} [options.groups]
 * @param {string[]} [options.selected]        pre-selected identities
 * @param {(selection: string[]) => void} [options.onChange]
 */
export function AudiencePicker({
  resolve,
  contacts = [],
  groups = [],
  selected = [],
  onChange = () => {},
} = {}) {
  /** Insertion-ordered so the chips do not reshuffle as you pick. */
  const selection = new Set(selected.map((s) => s.toLowerCase()));

  const chips = el("div", { class: "audience-chips", role: "list" });
  const contactList = el("div", { class: "audience-list" });
  const groupList = el("div", { class: "audience-groups" });

  const identityInput = IdentityInput({
    resolve,
    contacts,
    label: "Add by identity",
    preview: false,
    onSubmit: (identity) => { add(identity); identityInput.setValue(""); },
  });

  const root = el("div", { class: "audience-picker" }, [
    chips,
    groups.length && el("div", { class: "section-label", text: "Groups" }),
    groups.length && groupList,
    contacts.length && el("div", { class: "section-label", text: "Contacts" }),
    contacts.length ? contactList : el("p", { class: "muted small", text: "No contacts yet — type an identity below." }),
    identityInput.el,
  ]);

  function emit() {
    renderChips();
    renderContacts();
    renderGroups();
    onChange([...selection]);
  }

  function add(identity) {
    const name = String(identity || "").trim().toLowerCase();
    if (!name) return;
    selection.add(name);
    emit();
  }

  function remove(identity) {
    selection.delete(String(identity).toLowerCase());
    emit();
  }

  function toggle(identity) {
    if (selection.has(identity.toLowerCase())) remove(identity);
    else add(identity);
  }

  function renderChips() {
    clear(chips);
    if (!selection.size) {
      chips.append(el("p", { class: "muted small", text: "Nobody selected yet." }));
      return;
    }
    for (const identity of selection) {
      chips.append(el("span", { class: "audience-chip", role: "listitem" }, [
        avatar(identity, { size: "md" }),
        el("span", { class: "audience-chip-name", text: handleOf(identity), title: identity }),
        el("button", {
          class: "audience-chip-x",
          type: "button",
          "aria-label": `Remove ${identity}`,
          text: "×",
          onClick: () => remove(identity),
        }),
      ]));
    }
  }

  function renderContacts() {
    clear(contactList);
    for (const contact of contacts) {
      const picked = selection.has(contact.identity.toLowerCase());
      contactList.append(el("button", {
        class: `audience-row${picked ? " picked" : ""}`,
        type: "button",
        role: "checkbox",
        "aria-checked": String(picked),
        onClick: () => toggle(contact.identity),
      }, [
        avatar(contact.identity, { size: "md" }),
        el("span", { class: "audience-row-body" }, [
          el("span", { class: "audience-row-name", text: contact.petname || handleOf(contact.identity) }),
          el("span", { class: "audience-row-id small muted", text: contact.identity }),
        ]),
        el("span", { class: "audience-row-tick", text: picked ? "✓" : "" }),
      ]));
    }
  }

  function renderGroups() {
    clear(groupList);
    for (const group of groups) {
      const members = group.members ?? [];
      const all = members.length > 0 && members.every((m) => selection.has(m.toLowerCase()));
      groupList.append(el("button", {
        class: `audience-row${all ? " picked" : ""}`,
        type: "button",
        onClick: () => {
          // A group is a shortcut for its members, not a member itself: the
          // selection stays a flat list of identities either way.
          if (all) members.forEach((m) => selection.delete(m.toLowerCase()));
          else members.forEach((m) => selection.add(m.toLowerCase()));
          emit();
        },
      }, [
        el("span", { class: "audience-group-icon", text: "👥" }),
        el("span", { class: "audience-row-body" }, [
          el("span", { class: "audience-row-name", text: group.name || group.id }),
          el("span", { class: "audience-row-id small muted", text: `${members.length} member${members.length === 1 ? "" : "s"}` }),
        ]),
        el("span", { class: "audience-row-tick", text: all ? "✓" : "" }),
      ]));
    }
  }

  emit();

  return {
    el: root,
    selection: () => [...selection],
    add,
    remove,
    clear() { selection.clear(); emit(); },
  };
}
