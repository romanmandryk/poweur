/**
 * PolicyControls — who may reach this inbox, as one self-contained panel.
 *
 * It edits the two halves of `poweur-sys/relay/inbox-policy.json` together:
 * the mode (who may message at all) and the `anonymous` block (whether a
 * stranger with no identity may, and what it costs them). They live in one
 * document and mean nothing apart — "open" plus anonymous-denied is a very
 * different inbox from "open" plus anonymous-allowed at 8 bits.
 *
 * Free of app-shell imports, like the other components: EPIC-012's public
 * pages need the same vocabulary, and the caller owns saving.
 */

import { clear, el } from "./dom.js";

/** Plain language for each mode — the relay's rules, not a paraphrase. */
export const INBOX_MODES = [
  {
    id: "open",
    label: "Anyone",
    detail: "Any Poweur ID can message you. Simple, and the default for identities that never set a policy.",
  },
  {
    id: "contacts_only",
    label: "Contacts only",
    detail: "Only people you have accepted. Everyone else is refused — including contact requests, so nobody can ask.",
  },
  {
    id: "contacts_and_requests",
    label: "Contacts, and requests from others",
    detail: "Contacts message you normally; a stranger gets one contact request, which waits in your Requests tray. Recommended.",
  },
];

/**
 * What a difficulty costs *the sender's browser*, which is who pays it.
 *
 * Derived from the measured table in `apps/docs/docs/trust/anonymous-and-
 * challenges.md` (Go, one Apple-Silicon core) with that document's own
 * 5–10× penalty for browser JS applied — quoting the native numbers to
 * someone choosing a dial for web senders would understate it by an order
 * of magnitude.
 */
const POW_COST = [
  { bits: 8,  laptop: "instant",  phone: "instant" },
  { bits: 12, laptop: "instant",  phone: "instant" },
  { bits: 16, laptop: "~0.3 s",   phone: "~1 s" },
  { bits: 18, laptop: "~1 s",     phone: "~3 s" },
  { bits: 20, laptop: "~4 s",     phone: "~10 s" },
  { bits: 22, laptop: "~15 s",    phone: "~40 s" },
  { bits: 24, laptop: "~1 min",   phone: "~3 min" },
  { bits: 26, laptop: "~4 min",   phone: "~10 min" },
  { bits: 28, laptop: "~15 min",  phone: "~40 min" },
  { bits: 30, laptop: "~1 hour",  phone: "hours" },
];

/** The nearest measured row at or below `bits`. */
export function powCost(bits) {
  let match = POW_COST[0];
  for (const row of POW_COST) if (row.bits <= bits) match = row;
  return match;
}

/** Above this, a phone browser stops feeling like it is working. */
export const POW_UNCOMFORTABLE_BITS = 20;

export function describePowBits(bits) {
  const cost = powCost(bits);
  return `${bits} bits — about ${cost.laptop} on a laptop, ${cost.phone} on a phone`;
}

/**
 * @param {object} options
 * @param {{mode?: string, anonymous?: object}} [options.policy]  current document
 * @param {boolean} [options.explicit]   false when the relay default is standing in
 * @param {(policy: object) => Promise<void>|void} options.onSave
 * @param {boolean} [options.showSave]  false when the host supplies the button
 *   (the onboarding flow already has a "Continue" and two of them is one too
 *   many — it calls `save()` instead)
 */
export function PolicyControls({ policy = {}, explicit = true, onSave, showSave = true } = {}) {
  const state = {
    mode: policy.mode || "open",
    allow: Boolean(policy.anonymous?.allow),
    challenge: policy.anonymous?.challenge || "none",
    bits: policy.anonymous?.pow_bits || 16,
    maxBytes: policy.anonymous?.max_bytes || 4096,
    maxPerDay: policy.anonymous?.max_per_day || 20,
  };

  const modeList = el("div", { class: "policy-modes", role: "radiogroup", "aria-label": "Who can message you" });
  const anonBody = el("div", { class: "policy-anon-body" });
  const status = el("p", { class: "idin-status small", role: "status", "aria-live": "polite" });

  const allowToggle = el("input", {
    type: "checkbox",
    id: "policy-anon-allow",
    class: "policy-check",
    ...(state.allow ? { checked: true } : {}),
  });
  allowToggle.addEventListener("change", () => {
    state.allow = allowToggle.checked;
    renderAnon();
  });

  const save = el("button", { class: "btn btn-primary mt-md", id: "policy-save", text: "Save" });

  async function submit() {
    save.disabled = true;
    status.textContent = "Saving…";
    status.className = "idin-status small";
    try {
      await onSave(toDocument());
      status.textContent = "Saved";
      status.className = "idin-status small val-ok";
      return true;
    } catch (error) {
      status.textContent = error.message;
      status.className = "idin-status small val-warn";
      return false;
    } finally {
      save.disabled = false;
    }
  }
  save.addEventListener("click", submit);

  const root = el("div", { class: "policy-controls" }, [
    !explicit && el("p", {
      class: "muted small",
      text: "No policy saved yet — your relay is applying its default (anyone can message you).",
    }),
    el("div", { class: "section-label", text: "Who can message you" }),
    modeList,
    el("div", { class: "section-label", text: "Strangers with no identity" }),
    el("label", { class: "policy-toggle", for: "policy-anon-allow" }, [
      allowToggle,
      el("span", {}, [
        el("div", { class: "policy-toggle-label", text: "Allow anonymous messages" }),
        el("div", { class: "policy-toggle-detail muted small",
          text: "Unsigned, from nobody in particular. You cannot reply. Off unless you turn it on." }),
      ]),
    ]),
    anonBody,
    showSave && save,
    status,
  ]);

  function renderModes() {
    clear(modeList);
    for (const mode of INBOX_MODES) {
      const selected = state.mode === mode.id;
      const option = el("button", {
        class: `policy-mode${selected ? " selected" : ""}`,
        role: "radio",
        "aria-checked": String(selected),
        dataset: { mode: mode.id },
        onClick: () => { state.mode = mode.id; renderModes(); },
      }, [
        el("div", { class: "policy-mode-label", text: mode.label }),
        el("div", { class: "policy-mode-detail muted small", text: mode.detail }),
      ]);
      modeList.append(option);
    }
  }

  function renderAnon() {
    clear(anonBody);
    if (!state.allow) return;

    // "verified" and "payment" are designed policy slots the relay answers but
    // does not enforce (EPIC-014). Showing them disabled teaches the vocabulary
    // without promising a gate that is not there.
    const challenges = [
      { id: "none", label: "No challenge", enabled: true },
      { id: "pow", label: "Proof of work", enabled: true },
      { id: "verified", label: "Verified sender", enabled: false },
      { id: "payment", label: "Payment", enabled: false },
    ];
    const challengeRow = el("div", { class: "policy-challenges" },
      challenges.map((challenge) => el("button", {
        class: `chip policy-challenge${state.challenge === challenge.id ? " selected" : ""}`,
        dataset: { challenge: challenge.id },
        disabled: !challenge.enabled,
        title: challenge.enabled ? null : "Specified, not yet enforced by relays",
        text: challenge.enabled ? challenge.label : `${challenge.label} — soon`,
        onClick: () => { state.challenge = challenge.id; renderAnon(); },
      })));
    anonBody.append(el("div", { class: "section-label", text: "What it costs them" }), challengeRow);

    if (state.challenge === "pow") {
      const readout = el("div", { class: "policy-bits-readout small", id: "policy-bits-readout" });
      const slider = el("input", {
        type: "range",
        class: "policy-slider",
        id: "policy-pow-bits",
        min: "8",
        max: "30",
        step: "1",
        value: String(state.bits),
        "aria-label": "Proof-of-work difficulty in bits",
      });
      const warn = el("p", { class: "small val-warn", id: "policy-bits-warning", hidden: true,
        text: "Above 20 bits a phone browser feels stuck — most people give up rather than wait." });
      const paint = () => {
        readout.textContent = describePowBits(state.bits);
        warn.hidden = state.bits <= POW_UNCOMFORTABLE_BITS;
      };
      slider.addEventListener("input", () => {
        state.bits = Number(slider.value);
        paint();
      });
      paint();
      anonBody.append(slider, readout, warn);
    }

    const bytes = el("input", {
      type: "number", class: "input", id: "policy-max-bytes", min: "1", value: String(state.maxBytes),
    });
    bytes.addEventListener("input", () => { state.maxBytes = Number(bytes.value); });
    const perDay = el("input", {
      type: "number", class: "input", id: "policy-max-per-day", min: "1", value: String(state.maxPerDay),
    });
    perDay.addEventListener("input", () => { state.maxPerDay = Number(perDay.value); });

    anonBody.append(
      el("div", { class: "form-group mt-md" }, [
        el("label", { class: "form-label", for: "policy-max-bytes", text: "Longest message they can send (bytes)" }),
        bytes,
      ]),
      el("div", { class: "form-group" }, [
        el("label", { class: "form-label", for: "policy-max-per-day", text: "How many a day you will accept" }),
        perDay,
      ]),
    );
  }

  /** The document as the relay and the CLI expect it. */
  function toDocument() {
    const document = { version: 1, mode: state.mode };
    // Only write the block when it says something: an absent block is deny,
    // which is exactly what "not allowed" means.
    if (state.allow) {
      document.anonymous = {
        allow: true,
        challenge: state.challenge,
        max_bytes: state.maxBytes,
        max_per_day: state.maxPerDay,
        ...(state.challenge === "pow" ? { pow_bits: state.bits } : {}),
      };
    }
    return document;
  }

  renderModes();
  renderAnon();

  return { el: root, value: toDocument, save: submit, state };
}
