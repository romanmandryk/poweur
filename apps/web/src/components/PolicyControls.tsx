/**
 * PolicyControls — who may reach this inbox, as one self-contained panel
 * (from apps/web/js/components/policy-controls.js).
 *
 * Edits the mode and the `anonymous` block of `inbox-policy.json` together:
 * they live in one document and mean nothing apart. The caller owns saving.
 */
import { useImperativeHandle, useLayoutEffect, useRef, useState, type Ref } from "react";
import { cn } from "../lib/cn";
import {
  describePowBits,
  INBOX_MODES,
  POW_UNCOMFORTABLE_BITS,
  type InboxPolicy,
} from "../lib/policy";
import { Button } from "../ui/Button";
import { SectionLabel } from "../ui/Display";
import { CheckRow, FormGroup, Input, Label } from "../ui/Field";

export interface PolicyControlsHandle {
  /** The document as the relay and the CLI expect it. */
  value(): InboxPolicy;
  save(): Promise<boolean>;
}

interface PolicyState {
  mode: string;
  allow: boolean;
  challenge: string;
  bits: number;
  maxBytes: number;
  maxPerDay: number;
  readReceipts: boolean;
  disabledFor: string;
  trustedAuth: string;
}

function initialState(policy: InboxPolicy): PolicyState {
  return {
    mode: policy.mode || "open",
    allow: Boolean(policy.anonymous?.allow),
    challenge: policy.anonymous?.challenge || "none",
    bits: policy.anonymous?.pow_bits || 16,
    maxBytes: policy.anonymous?.max_bytes || 4096,
    maxPerDay: policy.anonymous?.max_per_day || 20,
    readReceipts: policy.read_receipts?.enabled !== false,
    disabledFor: (policy.read_receipts?.disabled_for ?? []).join(", "),
    trustedAuth: (policy.trusted_auth_services ?? []).join(", "),
  };
}

export function toPolicyDocument(state: PolicyState): InboxPolicy {
  const document: InboxPolicy = { version: 1, mode: state.mode };
  // Only write the block when it says something: an absent block is deny.
  if (state.allow) {
    document.anonymous = {
      allow: true,
      challenge: state.challenge,
      max_bytes: state.maxBytes,
      max_per_day: state.maxPerDay,
      ...(state.challenge === "pow" ? { pow_bits: state.bits } : {}),
    };
  }
  document.read_receipts = {
    enabled: state.readReceipts,
    disabled_for: state.disabledFor.split(",").map((value) => value.trim().toLowerCase()).filter(Boolean),
  };
  const trusted = [...new Set(state.trustedAuth.split(/[\s,]+/).map((value) => value.trim().toLowerCase()).filter(Boolean))];
  if (trusted.length) document.trusted_auth_services = trusted;
  return document;
}

// "verified" and "payment" are designed policy slots the relay answers but
// does not enforce (EPIC-014): shown disabled to teach the vocabulary.
const CHALLENGES = [
  { id: "none", label: "No challenge", enabled: true },
  { id: "pow", label: "Proof of work", enabled: true },
  { id: "verified", label: "Verified sender", enabled: false },
  { id: "payment", label: "Payment", enabled: false },
];

export function PolicyControls({
  ref,
  policy = {},
  explicit = true,
  onSave,
  showSave = true,
}: {
  ref?: Ref<PolicyControlsHandle>;
  policy?: InboxPolicy;
  /** false when the relay default is standing in. */
  explicit?: boolean;
  onSave: (policy: InboxPolicy) => Promise<void> | void;
  /** false when the host supplies the button and calls `save()` itself. */
  showSave?: boolean;
}) {
  const [state, setState] = useState(() => initialState(policy));
  const stateRef = useRef(state);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<{ text: string; tone: "" | "ok" | "warn" }>({ text: "", tone: "" });
  const onSaveRef = useRef(onSave);
  useLayoutEffect(() => {
    onSaveRef.current = onSave;
  });

  const update = (patch: Partial<PolicyState>) => {
    const next = { ...stateRef.current, ...patch };
    stateRef.current = next;
    setState(next);
  };

  async function submit(): Promise<boolean> {
    setSaving(true);
    setStatus({ text: "Saving…", tone: "" });
    try {
      await onSaveRef.current(toPolicyDocument(stateRef.current));
      setStatus({ text: "Saved", tone: "ok" });
      return true;
    } catch (error) {
      setStatus({ text: (error as Error).message, tone: "warn" });
      return false;
    } finally {
      setSaving(false);
    }
  }

  useImperativeHandle(ref, () => ({
    value: () => toPolicyDocument(stateRef.current),
    save: submit,
  }));

  return (
    <div className="policy-controls">
      {!explicit && (
        <p className="text-[13px] text-muted">
          No policy saved yet — your relay is applying its default (anyone can message you).
        </p>
      )}

      <SectionLabel className="px-0">Who can message you</SectionLabel>
      <div className="policy-modes mb-2 flex flex-col gap-2" role="radiogroup" aria-label="Who can message you">
        {INBOX_MODES.map((mode) => {
          const selected = state.mode === mode.id;
          return (
            <button
              key={mode.id}
              type="button"
              role="radio"
              aria-checked={selected}
              data-mode={mode.id}
              onClick={() => update({ mode: mode.id })}
              className={cn(
                "policy-mode min-h-11 rounded-card border-2 border-transparent bg-surface-2 px-3.5 py-3 text-left text-fg",
                selected && "selected border-accent bg-accent-soft",
              )}
            >
              <div className="policy-mode-label mb-0.5 text-[15px] font-semibold">{mode.label}</div>
              <div className="policy-mode-detail text-[13px] leading-snug text-muted">{mode.detail}</div>
            </button>
          );
        })}
      </div>

      <SectionLabel className="px-0">Strangers with no identity</SectionLabel>
      <CheckRow
        id="policy-anon-allow"
        checked={state.allow}
        onCheckedChange={(allow) => update({ allow })}
        label="Allow anonymous messages"
        detail="Unsigned, from nobody in particular. You cannot reply. Off unless you turn it on."
      />

      {state.allow && (
        <div className="policy-anon-body">
          <SectionLabel className="px-0">What it costs them</SectionLabel>
          <div className="policy-challenges mb-3 flex flex-wrap gap-2">
            {CHALLENGES.map((challenge) => (
              <button
                key={challenge.id}
                type="button"
                data-challenge={challenge.id}
                disabled={!challenge.enabled}
                title={challenge.enabled ? undefined : "Specified, not yet enforced by relays"}
                onClick={() => update({ challenge: challenge.id })}
                className={cn(
                  "chip policy-challenge inline-flex min-h-9 items-center rounded-full border border-sep bg-surface-2 px-2.5 py-[3px] text-xs font-bold text-fg",
                  "disabled:cursor-not-allowed disabled:opacity-45",
                  state.challenge === challenge.id && "selected border-accent bg-accent text-white",
                )}
              >
                {challenge.enabled ? challenge.label : `${challenge.label} — soon`}
              </button>
            ))}
          </div>

          {state.challenge === "pow" && (
            <>
              <input
                type="range"
                id="policy-pow-bits"
                className="policy-slider h-8 w-full accent-accent"
                min={8}
                max={30}
                step={1}
                value={state.bits}
                aria-label="Proof-of-work difficulty in bits"
                onChange={(event) => update({ bits: Number(event.currentTarget.value) })}
              />
              <div id="policy-bits-readout" className="policy-bits-readout mb-1 text-[13px] font-semibold text-fg">
                {describePowBits(state.bits)}
              </div>
              <p
                id="policy-bits-warning"
                className="val-warn text-[13px] text-warning"
                hidden={state.bits <= POW_UNCOMFORTABLE_BITS}
              >
                Above 20 bits a phone browser feels stuck — most people give up rather than wait.
              </p>
            </>
          )}

          <FormGroup className="mt-4">
            <Label htmlFor="policy-max-bytes">Longest message they can send (bytes)</Label>
            <Input
              id="policy-max-bytes"
              type="number"
              min={1}
              value={state.maxBytes}
              onChange={(event) => update({ maxBytes: Number(event.currentTarget.value) })}
            />
          </FormGroup>
          <FormGroup>
            <Label htmlFor="policy-max-per-day">How many a day you will accept</Label>
            <Input
              id="policy-max-per-day"
              type="number"
              min={1}
              value={state.maxPerDay}
              onChange={(event) => update({ maxPerDay: Number(event.currentTarget.value) })}
            />
          </FormGroup>
        </div>
      )}

      <SectionLabel className="px-0">Read receipts</SectionLabel>
      <CheckRow
        id="policy-read-receipts"
        checked={state.readReceipts}
        onCheckedChange={(readReceipts) => update({ readReceipts })}
        label="Send read receipts"
        detail="Adds a third tick only when you open a conversation."
      />
      <FormGroup>
        <Label htmlFor="policy-read-disabled-for">Never send to (comma-separated identities)</Label>
        <Input
          id="policy-read-disabled-for"
          type="text"
          value={state.disabledFor}
          placeholder="private-contact.example (optional)"
          onChange={(event) => update({ disabledFor: event.currentTarget.value })}
        />
      </FormGroup>

      <SectionLabel className="px-0">Sign-in services</SectionLabel>
      <FormGroup>
        <Label htmlFor="policy-trusted-auth">May send you sign-in requests (comma-separated)</Label>
        <Input
          id="policy-trusted-auth"
          type="text"
          value={state.trustedAuth}
          placeholder="bridge.poweur.org (optional)"
          onChange={(event) => update({ trustedAuth: event.currentTarget.value })}
        />
        <p className="mt-1 text-[13px] text-muted">
          A sign-in page shows the service's name. Listing it lets its requests reach your Sign-in requests — nothing
          else: it cannot message you or share files.
        </p>
      </FormGroup>

      {showSave && (
        <Button id="policy-save" className="mt-4" disabled={saving} onClick={() => void submit()}>
          Save
        </Button>
      )}
      <p
        role="status"
        aria-live="polite"
        className={cn(
          "idin-status min-h-[18px] text-[13px] text-muted",
          status.tone === "ok" && "val-ok text-success",
          status.tone === "warn" && "val-warn text-warning",
        )}
      >
        {status.text}
      </p>
    </div>
  );
}
