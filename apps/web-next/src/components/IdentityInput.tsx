/**
 * IdentityInput — type or search a Poweur ID (from
 * apps/web/js/components/identity-input.js).
 *
 * The single entry point for "who?" across the app: compose, add-contact and
 * the share audience picker all use it, so validation, contact autocomplete
 * and the resolved preview are written once. It answers with a *resolved*
 * identity, not a string. Free of app-shell imports (EPIC-012).
 *
 * The input is uncontrolled on purpose: the DOM value is the one truth, which
 * is what lets `lookup()` run synchronously from a submit handler without
 * waiting for React to commit a state update.
 */
import { useEffect, useId, useImperativeHandle, useLayoutEffect, useRef, useState, type Ref } from "react";
import { cn } from "../lib/cn";
import { handleOf, isValidIdentity } from "../lib/identity";
import { Avatar } from "../ui/Avatar";
import { inputClass, Label } from "../ui/Field";
import { ProfileCard, type ProfileEntry, type ResolveIdentity } from "./ProfileCard";

export interface ResolvedIdentity {
  identity: string;
  entry: ProfileEntry;
}

export interface ContactOption {
  identity: string;
  petname?: string | null;
}

export interface IdentityInputHandle {
  /** The resolved identity, or null when empty/invalid/unresolvable. */
  value(): ResolvedIdentity | null;
  /** The raw text, completed to a full ID — compose still lets you try. */
  raw(): string;
  setValue(next: string): Promise<ResolvedIdentity | null>;
  focus(): void;
  /** Force a lookup now (Enter, or a caller about to submit). */
  lookup(): Promise<ResolvedIdentity | null>;
  readonly input: HTMLInputElement | null;
}

export interface IdentityInputProps {
  ref?: Ref<IdentityInputHandle>;
  resolve: ResolveIdentity;
  contacts?: ContactOption[];
  value?: string;
  /** Complete a bare handle with this domain. */
  defaultDomain?: string;
  placeholder?: string;
  label?: string | null;
  /** Show a ProfileCard for the resolved ID. */
  preview?: boolean;
  onChange?: (result: ResolvedIdentity | null) => void;
  onSubmit?: (identity: string) => void;
  className?: string;
}

type Tone = "" | "ok" | "warn";

const DEBOUNCE_MS = 400;

export function IdentityInput({
  ref,
  resolve,
  contacts = [],
  value = "",
  defaultDomain = "",
  placeholder = "alice.poweur.net",
  label = null,
  preview = true,
  onChange,
  onSubmit,
  className,
}: IdentityInputProps) {
  const inputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [highlighted, setHighlighted] = useState(-1);
  const [status, setStatus] = useState<{ text: string; tone: Tone }>({ text: "", tone: "" });
  const [resolved, setResolved] = useState<ResolvedIdentity | null>(null);

  const resolvedRef = useRef<ResolvedIdentity | null>(null);
  const tokenRef = useRef(0); // guards against an earlier lookup landing last
  const debounceRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const latest = useRef({ resolve, onChange, onSubmit, defaultDomain });
  useLayoutEffect(() => {
    latest.current = { resolve, onChange, onSubmit, defaultDomain };
  });

  /**
   * What was typed, completed to a full Poweur ID. A hosted relay puts
   * everyone under one domain, so a bare handle is completed with the caller's
   * own — always visible in the field before anything is sent.
   */
  const current = () => {
    const raw = (inputRef.current?.value ?? "").trim().toLowerCase();
    const domain = latest.current.defaultDomain;
    if (!raw || raw.includes(".") || !domain) return raw;
    return `${raw}.${domain}`;
  };
  const completed = () => {
    const raw = (inputRef.current?.value ?? "").trim().toLowerCase();
    return Boolean(raw && !raw.includes(".") && latest.current.defaultDomain);
  };

  const setResult = (next: ResolvedIdentity | null) => {
    resolvedRef.current = next;
    setResolved(next);
  };

  async function lookup(): Promise<ResolvedIdentity | null> {
    // A forced lookup (Send, Enter) must cancel the input debounce, or a second
    // resolve ~400ms later bumps the token and this call returns null.
    clearTimeout(debounceRef.current);
    const identity = current();
    setResult(null);
    const report = latest.current.onChange;

    if (!identity) {
      setStatus({ text: "", tone: "" });
      report?.(null);
      return null;
    }
    if (!isValidIdentity(identity)) {
      setStatus({ text: "That does not look like a Poweur ID (try alice.poweur.net)", tone: "warn" });
      report?.(null);
      return null;
    }

    const token = ++tokenRef.current;
    setStatus({ text: "Looking up…", tone: "" });
    try {
      const entry = await latest.current.resolve(identity);
      if (token !== tokenRef.current) return null; // a newer lookup won
      const result = { identity, entry };
      setResult(result);
      setStatus({ text: completed() ? `Found ${identity}` : "Found", tone: "ok" });
      latest.current.onChange?.(result);
      return result;
    } catch (error) {
      if (token !== tokenRef.current) return null;
      setStatus({ text: `Not found: ${(error as Error).message}`, tone: "warn" });
      latest.current.onChange?.(null);
      return null;
    }
  }

  function choose(identity: string) {
    if (inputRef.current) inputRef.current.value = identity;
    setQuery(identity);
    setOpen(false);
    void lookup();
  }

  useImperativeHandle(ref, () => ({
    value: () => resolvedRef.current,
    raw: current,
    setValue(next: string) {
      if (inputRef.current) inputRef.current.value = next ?? "";
      setQuery("");
      setOpen(false);
      return lookup();
    },
    focus: () => inputRef.current?.focus(),
    lookup,
    get input() {
      return inputRef.current;
    },
  }));

  useEffect(() => {
    if (value) void lookup();
    return () => clearTimeout(debounceRef.current);
    // Mount only: `value` is the initial text of an uncontrolled field.
  }, []);

  const needle = query.trim().toLowerCase();
  const suggestions = open && needle
    ? contacts
        .filter((contact) => {
          const haystack = `${contact.identity} ${contact.petname ?? ""}`.toLowerCase();
          return haystack.includes(needle) && contact.identity.toLowerCase() !== needle;
        })
        .slice(0, 6)
    : [];
  const listId = `${inputId}-list`;
  const optionId = (index: number) => `${inputId}-opt-${index}`;

  return (
    <div className={cn("idin relative flex flex-col gap-1.5", className)}>
      {label && <Label htmlFor={inputId}>{label}</Label>}
      <input
        ref={inputRef}
        id={inputId}
        className={inputClass}
        type="text"
        defaultValue={value}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        inputMode="url"
        // A phone keyboard capitalises the first letter by default.
        autoCapitalize="none"
        autoCorrect="off"
        role="combobox"
        aria-expanded={suggestions.length > 0}
        aria-autocomplete="list"
        aria-controls={listId}
        aria-activedescendant={suggestions[highlighted] ? optionId(highlighted) : undefined}
        onInput={(event) => {
          setQuery(event.currentTarget.value);
          setOpen(true);
          setHighlighted(-1);
          setStatus({ text: "", tone: "" });
          clearTimeout(debounceRef.current);
          debounceRef.current = setTimeout(() => void lookup(), DEBOUNCE_MS);
        }}
        onBlur={() => setTimeout(() => setOpen(false), 120)}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            if (!suggestions.length) return;
            const delta = event.key === "ArrowDown" ? 1 : -1;
            setHighlighted((highlighted + delta + suggestions.length) % suggestions.length);
          } else if (event.key === "Escape") {
            setOpen(false);
          } else if (event.key === "Enter") {
            event.preventDefault();
            const active = suggestions[highlighted];
            if (active) {
              choose(active.identity);
              return;
            }
            void lookup().then((result) => {
              if (result) latest.current.onSubmit?.(result.identity);
            });
          }
        }}
      />
      <ul
        id={listId}
        role="listbox"
        hidden={suggestions.length === 0}
        className="idin-suggestions absolute inset-x-0 top-full z-40 max-h-65 list-none overflow-y-auto rounded-control border border-sep bg-surface shadow-pop"
      >
        {suggestions.map((contact, index) => (
          <li
            key={contact.identity}
            id={optionId(index)}
            role="option"
            aria-selected={index === highlighted}
            data-identity={contact.identity}
            className={cn(
              "idin-suggestion flex min-h-12 cursor-pointer items-center gap-2.5 px-3 py-2.5 hover:bg-surface-2",
              index === highlighted && "active bg-surface-2",
            )}
            // mousedown, not click: blur would close the list before click lands.
            onMouseDown={(event) => {
              event.preventDefault();
              choose(contact.identity);
            }}
          >
            <Avatar identity={contact.identity} size="md" />
            <span className="idin-suggestion-name text-[15px] font-semibold">{contact.petname || handleOf(contact.identity)}</span>
            <span className="idin-suggestion-id ml-auto text-[13px] text-muted">{contact.identity}</span>
          </li>
        ))}
      </ul>
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
      {preview && resolved && (
        <div className="idin-preview mt-2">
          <ProfileCard identity={resolved.identity} resolve={resolve} cached={resolved.entry} compact />
        </div>
      )}
    </div>
  );
}
