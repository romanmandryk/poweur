/**
 * @vitest-environment happy-dom
 *
 * The shared components (E15-T1, plus PolicyControls in E15-T3). They are the
 * epic's real deliverable — compose, add-contact, the share dialog, settings
 * and EPIC-012's public contact form all consume them — so they are tested
 * directly rather than through the shell.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";

import { IdentityInput, isValidIdentity } from "../js/components/identity-input.js";
import { AudiencePicker } from "../js/components/audience-picker.js";
import { ProfileCard } from "../js/components/profile-card.js";
import {
  PolicyControls, INBOX_MODES, describePowBits, powCost, POW_UNCOMFORTABLE_BITS,
} from "../js/components/policy-controls.js";
import { avatarColor, el, handleOf } from "../js/components/dom.js";

const entry = (identity, extra = {}) => ({
  identity,
  document: { identity, public_key: "ed25519:AAA", capabilities: ["messaging"] },
  profile: null,
  displayName: null,
  bio: null,
  links: [],
  avatar: null,
  capabilities: { version: 1, features: { messaging: "1" } },
  ...extra,
});

const resolveOk = (identity) => Promise.resolve(entry(identity));
const resolveFail = () => Promise.reject(new Error("identity not found"));

/** Let queued microtasks and the component's debounce settle. */
const settle = (ms = 0) => new Promise((r) => setTimeout(r, ms));

beforeEach(() => { document.body.innerHTML = ""; });

describe("isValidIdentity", () => {
  it("accepts FQDNs and rejects everything a user typos", () => {
    for (const good of ["alice.poweur.net", "a-b.example.com", "x.co"]) {
      expect(isValidIdentity(good), good).toBe(true);
    }
    for (const bad of ["", "alice", "alice.", ".net", "alice space.net", "-alice.net", "alice..net"]) {
      expect(isValidIdentity(bad), bad).toBe(false);
    }
  });

  it("is case-insensitive, matching the relay", () => {
    expect(isValidIdentity("Alice.Poweur.NET")).toBe(true);
  });
});

describe("ProfileCard", () => {
  it("paints a skeleton, then the resolved identity", async () => {
    const card = ProfileCard({ identity: "alice.poweur.net", resolve: resolveOk });
    expect(card.el.dataset.state).toBe("loading");
    expect(card.el.textContent).toContain("Resolving…");

    await card.ready;
    expect(card.el.dataset.state).toBe("ready");
    expect(card.el.textContent).toContain("alice.poweur.net");
    expect(card.el.textContent).toContain("messaging"); // capability chip
  });

  it("prefers the profile's display name over the handle", async () => {
    const card = ProfileCard({
      identity: "alice.poweur.net",
      resolve: (id) => Promise.resolve(entry(id, { displayName: "Alice Example", bio: "hi" })),
    });
    await card.ready;
    expect(card.el.querySelector(".profile-card-name").textContent).toBe("Alice Example");
    expect(card.el.textContent).toContain("hi");
  });

  it("says so when an identity cannot be resolved, instead of rendering nothing", async () => {
    const card = ProfileCard({ identity: "ghost.poweur.net", resolve: resolveFail });
    await card.ready;
    expect(card.el.dataset.state).toBe("error");
    expect(card.el.textContent).toContain("Could not resolve");
  });

  it("renders a contextual action and passes the identity back", async () => {
    const onSelect = vi.fn();
    const card = ProfileCard({
      identity: "alice.poweur.net",
      resolve: resolveOk,
      action: { label: "Message", onSelect },
    });
    await card.ready;
    card.el.querySelector(".profile-card-action").click();
    expect(onSelect).toHaveBeenCalledWith("alice.poweur.net", expect.objectContaining({ identity: "alice.poweur.net" }));
  });

  it("never renders a javascript: link from a resolved profile", async () => {
    const card = ProfileCard({
      identity: "evil.poweur.net",
      resolve: (id) => Promise.resolve(entry(id, {
        links: [{ label: "click", url: "javascript:alert(1)" }, { label: "ok", url: "https://example.com" }],
      })),
    });
    await card.ready;
    const hrefs = [...card.el.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual(["https://example.com"]);
  });

  it("escapes hostile text rather than parsing it as markup", async () => {
    const card = ProfileCard({
      identity: "evil.poweur.net",
      resolve: (id) => Promise.resolve(entry(id, { displayName: "<img src=x onerror=alert(1)>" })),
    });
    await card.ready;
    expect(card.el.querySelector("img")).toBeNull();
    expect(card.el.textContent).toContain("<img src=x onerror=alert(1)>");
  });
});

describe("IdentityInput", () => {
  it("rejects a malformed identity without calling the resolver", async () => {
    const resolve = vi.fn(resolveOk);
    const onChange = vi.fn();
    const input = IdentityInput({ resolve, onChange });

    input.input.value = "not an id";
    await input.lookup();

    expect(resolve).not.toHaveBeenCalled();
    expect(onChange).toHaveBeenCalledWith(null);
    expect(input.el.textContent).toContain("does not look like a Poweur ID");
    expect(input.value()).toBeNull();
  });

  it("resolves a valid identity and reports it", async () => {
    const onChange = vi.fn();
    const input = IdentityInput({ resolve: resolveOk, onChange, preview: false });

    input.input.value = "alice.poweur.net";
    await input.lookup();

    expect(input.value()).toMatchObject({ identity: "alice.poweur.net" });
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ identity: "alice.poweur.net" }));
    expect(input.el.textContent).toContain("Found");
  });

  it("surfaces an unresolvable identity instead of accepting it", async () => {
    const input = IdentityInput({ resolve: resolveFail, preview: false });
    input.input.value = "ghost.poweur.net";
    await input.lookup();
    expect(input.value()).toBeNull();
    expect(input.el.textContent).toContain("Not found");
  });

  it("shows a profile preview for what it resolved", async () => {
    const input = IdentityInput({ resolve: resolveOk });
    await input.setValue("alice.poweur.net");
    expect(input.el.querySelector(".profile-card")).toBeTruthy();
  });

  it("autocompletes from contacts and picks by pointer", async () => {
    const input = IdentityInput({
      resolve: resolveOk,
      preview: false,
      contacts: [
        { identity: "alice.poweur.net", petname: "Alice" },
        { identity: "bob.poweur.net", petname: "Bob" },
      ],
    });
    document.body.append(input.el);

    input.input.value = "ali";
    input.input.dispatchEvent(new Event("input"));

    const options = input.el.querySelectorAll(".idin-suggestion");
    expect(options).toHaveLength(1);
    expect(options[0].textContent).toContain("Alice");

    options[0].dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
    await settle();
    expect(input.input.value).toBe("alice.poweur.net");
    expect(input.value()).toMatchObject({ identity: "alice.poweur.net" });
  });

  it("keeps the last lookup's answer when an earlier one lands late", async () => {
    const slow = (identity) =>
      identity.startsWith("slow")
        ? new Promise((r) => setTimeout(() => r(entry(identity)), 40))
        : Promise.resolve(entry(identity));

    const input = IdentityInput({ resolve: slow, preview: false });
    input.input.value = "slow.poweur.net";
    const first = input.lookup();
    input.input.value = "fast.poweur.net";
    await input.lookup();
    await first;
    await settle(60);

    expect(input.value()).toMatchObject({ identity: "fast.poweur.net" });
  });

  it("keyboard-navigates the suggestion list", async () => {
    const input = IdentityInput({
      resolve: resolveOk,
      preview: false,
      contacts: [
        { identity: "alice.poweur.net", petname: "Alice" },
        { identity: "alina.poweur.net", petname: "Alina" },
      ],
    });
    input.input.value = "ali";
    input.input.dispatchEvent(new Event("input"));

    input.input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown" }));
    expect(input.el.querySelectorAll(".idin-suggestion")[0].classList.contains("active")).toBe(true);

    input.input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown" }));
    expect(input.el.querySelectorAll(".idin-suggestion")[1].classList.contains("active")).toBe(true);

    input.input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
    await settle();
    expect(input.input.value).toBe("alina.poweur.net");
  });
});

describe("AudiencePicker", () => {
  const contacts = [
    { identity: "alice.poweur.net", petname: "Alice" },
    { identity: "bob.poweur.net", petname: "Bob" },
  ];

  it("toggles contacts and reports the selection", () => {
    const onChange = vi.fn();
    const picker = AudiencePicker({ resolve: resolveOk, contacts, onChange });

    expect(picker.selection()).toEqual([]);
    expect(picker.el.textContent).toContain("Nobody selected yet");

    picker.el.querySelectorAll(".audience-row")[0].click();
    expect(picker.selection()).toEqual(["alice.poweur.net"]);
    expect(onChange).toHaveBeenLastCalledWith(["alice.poweur.net"]);

    picker.el.querySelectorAll(".audience-row")[0].click();
    expect(picker.selection()).toEqual([]);
  });

  it("expands a group into its members, and collapses it again", () => {
    const picker = AudiencePicker({
      resolve: resolveOk,
      contacts,
      groups: [{ id: "team", name: "Team", members: ["alice.poweur.net", "bob.poweur.net"] }],
    });

    const groupRow = picker.el.querySelector(".audience-groups .audience-row");
    groupRow.click();
    // A group is a shortcut for its members, not a member of the selection.
    expect(picker.selection().sort()).toEqual(["alice.poweur.net", "bob.poweur.net"]);

    picker.el.querySelector(".audience-groups .audience-row").click();
    expect(picker.selection()).toEqual([]);
  });

  it("removes a selection from its chip", () => {
    const picker = AudiencePicker({ resolve: resolveOk, contacts, selected: ["alice.poweur.net"] });
    expect(picker.selection()).toEqual(["alice.poweur.net"]);
    picker.el.querySelector(".audience-chip-x").click();
    expect(picker.selection()).toEqual([]);
  });

  it("de-duplicates case-insensitively so one person is picked once", () => {
    const picker = AudiencePicker({ resolve: resolveOk, contacts });
    picker.add("Alice.Poweur.NET");
    picker.add("alice.poweur.net");
    expect(picker.selection()).toEqual(["alice.poweur.net"]);
  });
});

describe("dom helpers", () => {
  it("sets text through the DOM rather than innerHTML", () => {
    const node = el("div", { text: "<b>not markup</b>" });
    expect(node.querySelector("b")).toBeNull();
    expect(node.textContent).toBe("<b>not markup</b>");
  });

  it("gives an identity a stable avatar colour", () => {
    expect(avatarColor("alice.poweur.net")).toBe(avatarColor("alice.poweur.net"));
    expect(handleOf("alice.poweur.net")).toBe("alice");
  });
});

describe("PolicyControls", () => {
  const save = () => Promise.resolve();

  it("offers every mode the relay implements, and marks the current one", () => {
    const controls = PolicyControls({ policy: { mode: "contacts_only" }, onSave: save });
    const options = controls.el.querySelectorAll(".policy-mode");
    expect(options).toHaveLength(INBOX_MODES.length);
    const selected = controls.el.querySelector(".policy-mode.selected");
    expect(selected.dataset.mode).toBe("contacts_only");
    expect(selected.getAttribute("aria-checked")).toBe("true");
  });

  it("writes no anonymous block at all when anonymous is off", () => {
    const controls = PolicyControls({ policy: { mode: "open" }, onSave: save });
    // An absent block *is* deny — writing `allow: false` would say the same
    // thing in a way the relay has to interpret.
    expect(controls.value()).toEqual({ version: 1, mode: "open" });
  });

  it("keeps the difficulty dial out of the document unless the challenge is pow", () => {
    const controls = PolicyControls({
      policy: { mode: "open", anonymous: { allow: true, challenge: "none", pow_bits: 22 } },
      onSave: save,
    });
    expect(controls.value().anonymous).toEqual({
      allow: true, challenge: "none", max_bytes: 4096, max_per_day: 20,
    });

    controls.el.querySelector('[data-challenge="pow"]').click();
    expect(controls.value().anonymous.pow_bits).toBe(22);
  });

  it("shows the two designed-but-unenforced challenges as disabled", () => {
    const controls = PolicyControls({
      policy: { mode: "open", anonymous: { allow: true } }, onSave: save,
    });
    expect(controls.el.querySelector('[data-challenge="verified"]').disabled).toBe(true);
    expect(controls.el.querySelector('[data-challenge="payment"]').disabled).toBe(true);
    expect(controls.el.querySelector('[data-challenge="pow"]').disabled).toBe(false);
  });

  it("describes the difficulty in time, and warns once a phone would suffer", () => {
    const controls = PolicyControls({
      policy: { mode: "open", anonymous: { allow: true, challenge: "pow", pow_bits: 16 } },
      onSave: save,
    });
    const slider = controls.el.querySelector("#policy-pow-bits");
    const warning = controls.el.querySelector("#policy-bits-warning");
    expect(controls.el.querySelector("#policy-bits-readout").textContent).toContain("16 bits");
    expect(warning.hidden).toBe(true);

    slider.value = "24";
    slider.dispatchEvent(new Event("input"));
    expect(controls.el.querySelector("#policy-bits-readout").textContent).toContain("24 bits");
    expect(warning.hidden).toBe(false);
    expect(controls.value().anonymous.pow_bits).toBe(24);
  });

  it("quotes browser cost, which is what the sender actually pays", () => {
    // The measured table is native Go; a browser is 5–10x slower, and the
    // person paying is on a browser.
    expect(powCost(16).phone).toBe("~1 s");
    expect(powCost(21).bits).toBe(20);
    expect(describePowBits(20)).toMatch(/laptop.*phone/);
    expect(POW_UNCOMFORTABLE_BITS).toBe(20);
  });

  it("hands the caller one document and reports a failed save", async () => {
    const saves = [];
    const controls = PolicyControls({
      policy: { mode: "open" },
      onSave: (document) => { saves.push(document); return Promise.reject(new Error("relay said no")); },
    });
    controls.el.querySelector('[data-mode="contacts_and_requests"]').click();
    controls.el.querySelector("#policy-save").click();
    await settle();

    expect(saves).toEqual([{ version: 1, mode: "contacts_and_requests" }]);
    expect(controls.el.querySelector(".idin-status").textContent).toBe("relay said no");
  });
});
