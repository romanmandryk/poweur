/**
 * A short code typed by hand (EPIC-028 E28-T9): eight cells, a dash drawn
 * between the fourth and the fifth. One real <input> sits under painted cells,
 * so paste, one-time-code autofill, IMEs and screen readers all see a single
 * ordinary text field.
 *
 * Typing fills a cell and moves on; typing over a filled cell replaces it;
 * Backspace clears the cell behind the caret; arrows and clicks move. Paste
 * fills from the first cell, with or without the dash. Crockford's alphabet:
 * lowercase becomes uppercase, O → 0, I and L → 1, anything else is refused
 * with a shake rather than inserted.
 */
import { useEffect, useRef, useState, type ClipboardEvent, type KeyboardEvent } from "react";
import { SHORT_ALPHABET, SHORT_CODE_LENGTH } from "@poweur/client";
import { cn } from "../lib/cn";

const SEPARATOR = /[\s\-_.\u2010-\u2015\u2212\uFE58\uFE63\uFF0D\u00A0\u200B-\u200D\uFEFF]/;
const SEPARATORS = new RegExp(SEPARATOR.source, "g");

/** One character as Crockford reads it, or "" if it is not in the alphabet. */
export function codeChar(raw: string): string {
  let c = raw.toUpperCase();
  if (c === "O") c = "0";
  else if (c === "I" || c === "L") c = "1";
  return c.length === 1 && SHORT_ALPHABET.includes(c) ? c : "";
}

/** Text as code characters, separators dropped; null if anything else is in it. */
export function codeChars(text: string): string | null {
  let out = "";
  for (const raw of String(text ?? "").replace(SEPARATORS, "")) {
    const c = codeChar(raw);
    if (!c) return null;
    out += c;
  }
  return out;
}

export interface CodeInputProps {
  id: string;
  label: string;
  /** Called with the eight characters, no dash, as soon as the last is typed. */
  onComplete: (code: string) => void;
  /** A whole link was pasted instead of a code. */
  onLink?: (link: string) => void;
  /** Any edit: clears an error shown beside the field. */
  onEdit?: () => void;
  invalid?: boolean;
  disabled?: boolean;
  autoFocus?: boolean;
}

export function CodeInput({ id, label, onComplete, onLink, onEdit, invalid, disabled, autoFocus }: CodeInputProps) {
  const input = useRef<HTMLInputElement>(null);
  const [value, setValue] = useState("");
  const [caret, setCaret] = useState(0);
  // Events can arrive faster than renders (a paste, autofill, quick typing):
  // handlers read and write these, never the render's copies.
  const live = useRef({ value: "", caret: 0 });
  const [focused, setFocused] = useState(false);
  const [shake, setShake] = useState(0);
  const length = SHORT_CODE_LENGTH;
  const group = length / 2;

  // Keep the real caret where the painted one is.
  useEffect(() => {
    const el = input.current;
    if (el && document.activeElement === el) el.setSelectionRange(caret, caret);
  }, [caret, value]);

  const refuse = () => setShake((n) => n + 1);
  const moveTo = (at: number) => {
    live.current.caret = Math.max(0, Math.min(at, live.current.value.length));
    setCaret(live.current.caret);
  };
  const commit = (next: string, at: number) => {
    const clipped = next.slice(0, length);
    const changed = clipped !== live.current.value;
    live.current.value = clipped;
    setValue(clipped);
    moveTo(at);
    if (!changed) return;
    onEdit?.();
    if (clipped.length === length) onComplete(clipped);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    // The painted caret is the truth; the field's own only counts for a
    // selection (React moves the native caret whenever it sets the value).
    const el = event.currentTarget;
    const value = live.current.value;
    const selected = el.selectionStart !== null && el.selectionEnd !== null && el.selectionStart !== el.selectionEnd;
    const start = selected ? el.selectionStart! : Math.min(live.current.caret, value.length);
    const end = selected ? el.selectionEnd! : start;
    if (event.key === "Backspace") {
      event.preventDefault();
      if (start !== end) commit(value.slice(0, start) + value.slice(end), start);
      else if (start > 0) commit(value.slice(0, start - 1) + value.slice(start), start - 1);
      return;
    }
    if (event.key === "Delete") {
      event.preventDefault();
      commit(value.slice(0, start) + value.slice(Math.max(end, start + 1)), start);
      return;
    }
    const moves: Record<string, number> = { ArrowLeft: start - 1, ArrowRight: start + 1, Home: 0, End: value.length };
    if (event.key in moves) {
      event.preventDefault();
      moveTo(moves[event.key]!);
      return;
    }
    if (event.key.length !== 1) return; // Tab, Enter, and keyboards that say "Unidentified"
    event.preventDefault();
    if (SEPARATOR.test(event.key)) return; // a typed dash or space is simply not needed
    const c = codeChar(event.key);
    if (!c) return refuse();
    // Over a filled cell, replace it; at the end, append.
    const next = value.slice(0, start) + c + value.slice(start === end ? start + 1 : end);
    commit(next, start + 1);
  };

  // Mobile keyboards and autofill change the value without a usable keydown.
  const onChange = (raw: string) => {
    const next = codeChars(raw);
    if (next === null) return refuse();
    commit(next, input.current?.selectionStart ?? next.length);
  };

  const onPaste = (event: ClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData.getData("text").trim();
    event.preventDefault();
    if (onLink && (/#pair=|^poweur:/i.test(text) || /^https?:\/\//i.test(text))) {
      onLink(text);
      return;
    }
    const next = codeChars(text);
    if (next === null || next.length === 0) return refuse();
    commit(next, next.length);
  };

  const active = focused ? Math.min(caret, length - 1) : -1;
  const cells = Array.from({ length }, (_, i) => value[i] ?? "");
  return (
    <div
      key={shake}
      className={cn("code-input relative mx-auto w-fit", shake > 0 && "animate-shake")}
      data-testid={`${id}-cells`}
    >
      <div className="flex items-center gap-1.5" aria-hidden="true">
        {cells.map((c, i) => (
          <div key={i} className="contents">
            {i === group && <span className="px-0.5 text-2xl font-bold text-muted">–</span>}
            <span
              className={cn(
                "code-cell flex h-14 w-10 items-center justify-center rounded-control border-[1.5px] bg-surface font-mono text-2xl font-bold text-fg",
                i === active ? "border-accent shadow-[0_0_0_3px_var(--color-accent-soft)]" : "border-sep",
                invalid && "border-danger",
              )}
            >
              {c}
            </span>
          </div>
        ))}
      </div>
      <input
        ref={input}
        id={id}
        aria-label={label}
        aria-invalid={invalid || undefined}
        type="text"
        inputMode="text"
        autoComplete="one-time-code"
        autoCapitalize="characters"
        autoCorrect="off"
        spellCheck={false}
        disabled={disabled}
        autoFocus={autoFocus}
        value={value}
        className="absolute inset-0 h-full w-full cursor-text opacity-0"
        onKeyDown={onKeyDown}
        onChange={(event) => onChange(event.currentTarget.value)}
        onPaste={onPaste}
        onPointerUp={(event) => moveTo(event.currentTarget.selectionStart ?? live.current.value.length)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
      />
    </div>
  );
}
