/**
 * The name field, shared by the landing and the `claim` sub-page (E15-T8/T10).
 * Self-hosting is a relay of your own, not a form here: the landing links to
 * the self-hosting guide instead of collecting DNS credentials.
 *
 * `#ni-handle`, `#ni-availability` and `#btn-claim` are the contract the e2e
 * specs use. The fields are uncontrolled, as in the legacy app: the DOM value
 * is what a claim reads, and the relay-refused path writes the attempt back.
 */
import { useEffect, useRef, useState, type ReactNode } from "react";
import { chooseCustody, createIdentity, type ClaimIntent } from "../../actions/identity";
import { cn } from "../../lib/cn";
import { claimInvite, normalizeHandleInput, policyHint, webCustodyBlocked } from "../../lib/claim";
import { identityApiFor } from "../../lib/client.js";
import { checkPasskeySupport, PRF_UNAVAILABLE_MESSAGE } from "../../lib/passkey.js";
import { LEGAL_LINKS, showsPoweurLegal } from "../../lib/legal";
import { defaultRelayUrl } from "../../lib/storage.js";
import { useData } from "../../state/data";
import { useSession, type ModeInfo } from "../../state/session";
import { toast } from "../../state/ui";
import { Button } from "../../ui/Button";
import { Skeleton } from "../../ui/Display";
import { Note } from "../../ui/Field";
import { cardClass } from "./DoorPage";

const AVAILABILITY_DEBOUNCE_MS = 350;

/** Passkey and custody support, probed once and said before the name (E15-T12). */
function useCustodySupport() {
  const passkey = useData((state) => state.passkey);
  const custody = useData((state) => state.custody);
  useEffect(() => {
    if (!passkey) void checkPasskeySupport().then((support: unknown) => useData.setState({ passkey: support }));
  }, [passkey]);
  useEffect(() => {
    if (!custody) void chooseCustody().then((choice) => useData.setState({ custody: choice }));
  }, [custody]);
  return { passkey, custody, blocked: webCustodyBlocked(custody, passkey) };
}

type Status = { text: string; tone: "" | "ok" | "warn" } | null;

/**
 * Check the handle as the user types (EPIC-018 E18-T3). "That name is taken"
 * must arrive *before* a passkey ceremony the user cannot get back, so the
 * button stays disabled until the relay has said yes.
 */
function useAvailability(readIntent: () => ClaimIntent, blocked: boolean) {
  const [status, setStatus] = useState<Status>(null);
  const [pending, setPending] = useState(false);
  const [available, setAvailable] = useState<boolean | null>(null);
  const token = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const check = async () => {
    const intent = readIntent();
    const mine = ++token.current;
    // Self-hosted names are not this relay's to give out; empty is no question.
    if (!intent.hosted || !intent.handle) {
      setPending(false);
      setAvailable(null);
      setStatus(intent.handle ? { text: "", tone: "" } : null);
      return;
    }
    setPending(true);
    setStatus({ text: "Checking…", tone: "" });
    try {
      const verdict: any = await (identityApiFor(defaultRelayUrl()) as any).availability(intent.handle, intent.domain);
      if (mine !== token.current) return; // a later keystroke won
      if (verdict.policy) useData.setState((state) => ({ door: { ...state.door, policy: verdict.policy } }));
      setPending(false);
      setAvailable(Boolean(verdict.available));
      setStatus(
        verdict.available ? { text: `${verdict.identity} is available`, tone: "ok" } : { text: verdict.message, tone: "warn" },
      );
    } catch (error) {
      if (mine !== token.current) return;
      // Registration is still the authority; a relay that cannot answer must
      // not block the flow.
      setPending(false);
      setAvailable(null);
      console.warn("Availability check failed:", (error as Error).message);
    }
  };

  const schedule = () => {
    clearTimeout(timer.current);
    timer.current = setTimeout(() => void check(), AVAILABILITY_DEBOUNCE_MS);
  };

  const onEdited = () => {
    setPending(true);
    setAvailable(null);
    setStatus({ text: "", tone: "" });
    schedule();
  };

  useEffect(() => () => clearTimeout(timer.current), []);

  return { status, schedule, onEdited, disabled: blocked || pending || available === false };
}

function ClaimNote({ info, passkey, custody, blocked, hero }: { info: ModeInfo; passkey: any; custody: any; blocked: boolean; hero: boolean }) {
  const base = cn("claim-note mt-2.5 text-[13px]", hero && "mx-auto max-w-[360px] text-white/65");
  const warn = hero ? "val-warn text-amber-200" : "val-warn text-warning";
  if (info.reachable === false && info.resolved) {
    return <Note className={cn(base, warn)}>Can't reach the relay right now — names can't be checked until it answers.</Note>;
  }
  if (custody?.kind === "native") {
    return (
      <Note className={base}>
        This device's keystore will hold your key, and only your biometrics release it. Nothing leaves the device in plain form.
      </Note>
    );
  }
  if (blocked) {
    return (
      <Note id="claim-prf-required" className={cn(base, warn)}>
        {passkey?.reason || PRF_UNAVAILABLE_MESSAGE}
      </Note>
    );
  }
  return (
    <Note className={base}>
      Your keys are generated on this device and never leave it in plain form. This browser must support passkeys with PRF (Apple or
      Google passkeys in Safari or Chrome).
    </Note>
  );
}

function StatusLine({ status, fallback, className, hero = false }: { status: Status; fallback: string; className?: string; hero?: boolean }) {
  const shown = status ?? { text: fallback, tone: "" as const };
  return (
    <p
      id="ni-availability"
      role="status"
      aria-live="polite"
      aria-describedby={undefined}
      className={cn(
        "idin-status mt-2 min-h-5 text-[13px] text-muted",
        hero && "text-white/70",
        shown.tone === "ok" && (hero ? "val-ok text-emerald-300" : "val-ok text-success"),
        shown.tone === "warn" && (hero ? "val-warn text-amber-200" : "val-warn text-warning"),
        className,
      )}
    >
      {shown.text}
    </p>
  );
}

function refocus(field: HTMLInputElement | null, value: string) {
  if (!field) return;
  field.value = value;
  field.focus();
  field.select?.();
}

/**
 * poweur.org's closing call to action: the violet night box with the field and
 * its button joined into one control. The landing draws the claim this way;
 * the in-app sub-page keeps the plain card.
 */
const heroClass =
  "claim-hero relative animate-fade-in-up overflow-hidden rounded-[24px] border border-white/12 px-5 pt-8 pb-6 text-center text-white " +
  "bg-[radial-gradient(75%_90%_at_50%_0%,var(--color-violet-600)_0%,var(--color-indigo-800)_50%,var(--color-indigo-950)_100%)] " +
  "shadow-[0_24px_60px_-20px_color-mix(in_srgb,var(--color-violet-700)_60%,transparent)]";

/** The hosted claim: a handle with the domain as a suffix inside the field. */
export function ClaimCard({ info, hero = false }: { info: ModeInfo; hero?: boolean }) {
  const config = useSession((state) => state.config);
  const policy = useData((state) => state.door.policy);
  const { passkey, custody, blocked } = useCustodySupport();
  const handleRef = useRef<HTMLInputElement>(null);
  const domainRef = useRef<HTMLInputElement & HTMLSelectElement>(null);
  const domains = info.hostedDomains ?? [];

  const readIntent = (): ClaimIntent => {
    const domain = (domainRef.current?.value ?? (domains.length === 1 ? domains[0] : info.domain) ?? "")
      .trim()
      .toLowerCase()
      .replace(/^\.+/, "");
    return {
      handle: normalizeHandleInput(handleRef.current?.value ?? "", info),
      domain,
      hosted: domains.includes(domain),
    };
  };
  const availability = useAvailability(readIntent, blocked);
  const [invite] = useState(() => claimInvite(globalThis.location?.search ?? ""));

  // A link from a sign-in page names the handle it checked: fill it and ask
  // again — it may have gone since.
  useEffect(() => {
    const field = handleRef.current;
    if (!info.probed || !field || !invite.handle || field.value) return;
    field.value = invite.handle;
    availability.onEdited();
  }, [info.probed, invite.handle]);

  // The field's shape depends on what the relay hosts; a skeleton is the
  // honest frame for "about to know" (E15-T12).
  if (!info.probed) {
    return (
      <div id="claim-card-pending" className={cn("claim-card", hero ? heroClass : cardClass)}>
        {hero && <ClaimHeading domain={domains[0] ?? info.domain} />}
        <div className="skeleton-stack my-4 flex w-full flex-col gap-2.5" aria-hidden="true">
          <Skeleton className={cn("h-13 w-full rounded-button", hero && "bg-white/15")} />
          <Skeleton className={cn("h-3.5 w-3/5 self-center", hero && "bg-white/15")} />
        </div>
        <p role="status" className={cn("text-[13px] text-muted", hero && "text-white/70")}>
          Connecting…
        </p>
      </div>
    );
  }

  const claim = () => {
    if (blocked) {
      toast(passkey?.reason || PRF_UNAVAILABLE_MESSAGE, "error", 9000);
      return;
    }
    const intent = readIntent();
    if (!intent.handle) {
      refocus(handleRef.current, "");
      toast("Choose a name", "warning");
      return;
    }
    if (!intent.domain) {
      toast("Enter the domain your identity lives under", "warning");
      return;
    }
    void createIdentity(intent, { onNameRefused: (handle) => refocus(handleRef.current, handle) });
  };

  let suffix: ReactNode;
  const suffixClass = cn(
    "claim-suffix flex max-w-[55%] shrink-0 items-center truncate pr-3.5 text-[17px] whitespace-nowrap text-muted",
    hero && "font-mono text-[15px] text-white/60",
  );
  if (domains.length > 1) {
    suffix = (
      <select
        ref={domainRef}
        id="ni-domain"
        aria-label="Domain"
        defaultValue={info.domain}
        onChange={availability.schedule}
        className={cn(suffixClass, "claim-suffix-select cursor-pointer border-none bg-transparent")}
      >
        {domains.map((domain) => (
          <option key={domain} value={domain} className="text-fg">
            .{domain}
          </option>
        ))}
      </select>
    );
  } else if (domains.length === 1) {
    suffix = (
      <span id="ni-domain-fixed" data-domain={domains[0]} className={suffixClass}>
        .{domains[0]}
      </span>
    );
  } else {
    // Nothing hosted here: the domain is genuinely unknown, so it is typed.
    suffix = (
      <input
        ref={domainRef}
        id="ni-domain"
        type="text"
        aria-label="Parent domain"
        placeholder="example.org"
        defaultValue={info.domain || config.parentDomain || ""}
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        onInput={availability.schedule}
        className={cn(
          "claim-suffix-input min-w-0 border-l border-sep bg-transparent px-3 text-base text-fg outline-none",
          hero && "border-white/20 text-white placeholder:text-white/40",
        )}
      />
    );
  }

  const input = (
    <input
      ref={handleRef}
      id="ni-handle"
      type="text"
      placeholder="yourname"
      autoComplete="off"
      spellCheck={false}
      autoCapitalize="none"
      autoCorrect="off"
      enterKeyHint="go"
      aria-describedby="ni-availability"
      className={cn(
        "claim-input min-w-0 flex-1 border-none bg-transparent py-[15px] pr-1 pl-3.5 text-lg font-semibold text-fg outline-none placeholder:font-normal placeholder:text-faint",
        hero && "pl-4 font-mono text-base font-medium text-white placeholder:text-white/40",
      )}
      onInput={(event) => {
        // A pasted `alice.poweur.net` or `@alice` means `alice`.
        const field = event.currentTarget;
        const cleaned = normalizeHandleInput(field.value, info);
        if (cleaned !== field.value) field.value = cleaned;
        availability.onEdited();
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter") claim();
      }}
    />
  );

  const fromSignIn = invite.fromSignIn && (
    <p
      id="claim-from-signin"
      className={cn("mb-3 rounded-control bg-accent-soft px-3 py-2 text-[13px] text-fg", hero && "mt-5 bg-white/12 text-left text-white")}
    >
      A sign-in is waiting in your other tab. Create your ID here, then go back to that tab — it picks up your new ID.
    </p>
  );

  if (hero) {
    return (
      <div id="claim-card" className={cn("claim-card", heroClass)}>
        <ClaimHeading domain={domains[0] ?? info.domain} />
        {fromSignIn}
        <label htmlFor="ni-handle" className="claim-label sr-only">
          Choose your name
        </label>
        {/* One control, as on poweur.org: the name, its domain and the button
            share a border. Below 400px the button wraps under the field. */}
        <div className="claim-field mt-6 flex flex-wrap items-stretch overflow-hidden rounded-xl border border-white/20 bg-black/35 text-left transition-colors focus-within:border-white/50">
          {input}
          {suffix}
          <Button
            id="btn-claim"
            className="claim-submit h-[52px] basis-full rounded-none bg-white px-6 py-0 text-base text-[#0b0a10] min-[400px]:basis-auto min-[400px]:w-auto"
            disabled={availability.disabled}
            onClick={claim}
          >
            Claim
          </Button>
        </div>
        <StatusLine hero status={availability.status} fallback={policyHint(info, policy)} />
        <ClaimNote hero info={info} passkey={passkey} custody={custody} blocked={blocked} />
        {showsPoweurLegal(domains) && <ClaimAgreement hero />}
      </div>
    );
  }

  return (
    <div id="claim-card" className={cn("claim-card", cardClass)}>
      {fromSignIn}
      <label htmlFor="ni-handle" className="claim-label mb-2 block text-[13px] font-semibold text-muted">
        Choose your name
      </label>
      <div className="claim-field flex items-stretch overflow-hidden rounded-control border-[1.5px] border-transparent bg-surface-2 transition-colors focus-within:border-accent focus-within:bg-surface">
        {input}
        {suffix}
      </div>
      <StatusLine status={availability.status} fallback={policyHint(info, policy)} />
      <Button id="btn-claim" className="claim-submit mt-3.5" disabled={availability.disabled} onClick={claim}>
        Create ID
      </Button>
      <ClaimNote hero={false} info={info} passkey={passkey} custody={custody} blocked={blocked} />
      {showsPoweurLegal(domains) && <ClaimAgreement hero={false} />}
    </div>
  );
}

/** Creating a hosted ID is agreeing to the service's legal documents; say so where it happens. */
export function ClaimAgreement({ hero }: { hero: boolean }) {
  const link = cn("underline underline-offset-2", hero ? "text-white/80 hover:text-white" : "text-muted hover:text-fg");
  return (
    <p id="claim-agreement" className={cn("claim-agreement mt-2 text-center text-xs text-faint", hero && "text-white/55")}>
      By creating an ID you agree to the{" "}
      <a href={LEGAL_LINKS.terms} target="_blank" rel="noopener" className={link}>
        Terms
      </a>{" "}
      and{" "}
      <a href={LEGAL_LINKS.privacy} target="_blank" rel="noopener" className={link}>
        Privacy Policy
      </a>
      .
    </p>
  );
}

function ClaimHeading({ domain }: { domain: string }) {
  return (
    <>
      <h2 className="claim-title text-[26px] leading-[1.12] font-semibold tracking-[-.6px] text-balance">
        Claim your name on the open internet.
      </h2>
      <p className="claim-lede mx-auto mt-3 max-w-[340px] text-[15px] leading-normal text-white/75">
        {domain ? `Free hosted IDs on ${domain}.` : "Free hosted IDs."} Pick a name in seconds.
      </p>
    </>
  );
}

