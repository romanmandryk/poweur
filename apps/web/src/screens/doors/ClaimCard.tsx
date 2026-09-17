/**
 * The name field, shared by the landing and the `claim` sub-page (E15-T8/T10).
 *
 * `#ni-handle`, `#ni-availability` and `#btn-claim` are the contract the e2e
 * specs use. The fields are uncontrolled, as in the legacy app: the DOM value
 * is what a claim reads, and the relay-refused path writes the attempt back.
 */
import { useEffect, useRef, useState, type ReactNode } from "react";
import { KeyRound } from "lucide-react";
import { chooseCustody, createIdentity, type ClaimIntent } from "../../actions/identity";
import { cn } from "../../lib/cn";
import { claimInvite, normalizeHandleInput, policyHint, webCustodyBlocked } from "../../lib/claim";
import { identityApiFor } from "../../lib/client.js";
import { checkPasskeySupport, PRF_UNAVAILABLE_MESSAGE } from "../../lib/passkey.js";
import { defaultRelayUrl } from "../../lib/storage.js";
import { useData } from "../../state/data";
import { useSession, type ModeInfo } from "../../state/session";
import { toast } from "../../state/ui";
import { Button } from "../../ui/Button";
import { Skeleton } from "../../ui/Display";
import { FormGroup, Input, inputClass, Label, Note } from "../../ui/Field";
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

function ClaimNote({ info, passkey, custody, blocked }: { info: ModeInfo; passkey: any; custody: any; blocked: boolean }) {
  const base = "claim-note mt-2.5 text-[13px]";
  if (info.reachable === false && info.resolved) {
    return <Note className={cn(base, "val-warn text-warning")}>Can't reach the relay right now — names can't be checked until it answers.</Note>;
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
      <Note id="claim-prf-required" className={cn(base, "val-warn text-warning")}>
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

function StatusLine({ status, fallback, className }: { status: Status; fallback: string; className?: string }) {
  const shown = status ?? { text: fallback, tone: "" as const };
  return (
    <p
      id="ni-availability"
      role="status"
      aria-live="polite"
      aria-describedby={undefined}
      className={cn(
        "idin-status mt-2 min-h-5 text-[13px] text-muted",
        shown.tone === "ok" && "val-ok text-success",
        shown.tone === "warn" && "val-warn text-warning",
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

/** The hosted claim: a handle with the domain as a suffix inside the field. */
export function ClaimCard({ info }: { info: ModeInfo }) {
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
      <div id="claim-card-pending" className={cn("claim-card", cardClass)}>
        <div className="skeleton-stack my-4 flex w-full flex-col gap-2.5" aria-hidden="true">
          <Skeleton className="h-13 w-full rounded-button" />
          <Skeleton className="h-3.5 w-3/5 self-center" />
        </div>
        <p role="status" className="text-[13px] text-muted">
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
  const suffixClass = "claim-suffix flex max-w-[55%] shrink-0 items-center truncate pr-3.5 text-[17px] whitespace-nowrap text-muted";
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
          <option key={domain} value={domain}>
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
        className="claim-suffix-input min-w-0 border-l border-sep bg-transparent px-3 text-base text-fg outline-none"
      />
    );
  }

  return (
    <div id="claim-card" className={cn("claim-card", cardClass)}>
      {invite.fromSignIn && (
        <p id="claim-from-signin" className="mb-3 rounded-control bg-accent-soft px-3 py-2 text-[13px] text-fg">
          A sign-in is waiting in your other tab. Create your ID here, then go back to that tab — it picks up your new ID.
        </p>
      )}
      <label htmlFor="ni-handle" className="claim-label mb-2 block text-[13px] font-semibold text-muted">
        Choose your name
      </label>
      <div className="claim-field flex items-stretch overflow-hidden rounded-control border-[1.5px] border-transparent bg-surface-2 transition-colors focus-within:border-accent focus-within:bg-surface">
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
          className="claim-input min-w-0 flex-1 border-none bg-transparent py-[15px] pr-1 pl-3.5 text-lg font-semibold text-fg outline-none placeholder:font-normal placeholder:text-faint"
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
        {suffix}
      </div>
      <StatusLine status={availability.status} fallback={policyHint(info, policy)} />
      <Button id="btn-claim" className="claim-submit mt-3.5" disabled={availability.disabled} onClick={claim}>
        Create ID
      </Button>
      <ClaimNote info={info} passkey={passkey} custody={custody} blocked={blocked} />
    </div>
  );
}

/**
 * The self-hosted path: the relay writes the DNS records with a scoped token
 * that is never stored. A different intent, not a checkbox (E15-T10).
 */
export function DnsClaimCard({ info }: { info: ModeInfo }) {
  const config = useSession((state) => state.config);
  const { blocked, passkey } = useCustodySupport();
  const form = useRef<HTMLDivElement>(null);
  const field = (id: string) => form.current?.querySelector<HTMLInputElement & HTMLSelectElement>(`#${id}`) ?? null;

  const readIntent = (): ClaimIntent => {
    const domain = (field("ni-domain")?.value ?? "").trim().toLowerCase().replace(/^\.+/, "");
    return {
      handle: normalizeHandleInput(field("ni-handle")?.value ?? "", info),
      domain,
      hosted: (info.hostedDomains ?? []).includes(domain),
      provider: field("ni-provider")?.value,
      dnsToken: field("ni-token")?.value.trim() ?? "",
      inviteCode: field("ni-invite")?.value.trim() ?? "",
    };
  };
  const availability = useAvailability(readIntent, blocked);

  const claim = () => {
    if (blocked) {
      toast(passkey?.reason || PRF_UNAVAILABLE_MESSAGE, "error", 9000);
      return;
    }
    const intent = readIntent();
    if (!intent.handle) {
      refocus(field("ni-handle"), "");
      toast("Choose a name", "warning");
      return;
    }
    if (!intent.domain) {
      toast("Enter the domain your identity lives under", "warning");
      return;
    }
    void createIdentity(intent, { onNameRefused: (handle) => refocus(field("ni-handle"), handle) });
  };

  const textProps = { autoComplete: "off", spellCheck: false, autoCapitalize: "none", autoCorrect: "off" } as const;

  return (
    <div ref={form} id="claim-card" className="form-card rounded-card bg-surface px-4 py-5">
      <p className="mb-4 text-[13px] text-muted">
        Your identity lives under a domain you control. The relay writes the DNS records for you with a scoped API token, which is never
        stored.
      </p>
      <FormGroup>
        <Label htmlFor="ni-handle">Name</Label>
        <Input id="ni-handle" type="text" placeholder="yourname" {...textProps} onInput={availability.onEdited} />
      </FormGroup>
      <FormGroup>
        <Label htmlFor="ni-domain">Your domain</Label>
        <Input
          id="ni-domain"
          type="text"
          placeholder="example.org"
          defaultValue={config.parentDomain || ""}
          {...textProps}
          onInput={availability.schedule}
        />
      </FormGroup>
      <FormGroup>
        <Label htmlFor="ni-provider">DNS provider</Label>
        <select id="ni-provider" className={cn(inputClass, "cursor-pointer")} defaultValue={config.dnsProvider || "cloudflare"}>
          <option value="cloudflare">Cloudflare</option>
          <option value="hetzner">Hetzner</option>
        </select>
      </FormGroup>
      <FormGroup>
        <Label htmlFor="ni-token">DNS API token</Label>
        <Input id="ni-token" type="password" placeholder="Scoped API token" autoComplete="off" />
      </FormGroup>
      <FormGroup>
        <Label htmlFor="ni-invite">Invite code (if this relay asks for one)</Label>
        <Input id="ni-invite" type="text" placeholder="optional" autoComplete="off" />
      </FormGroup>
      <StatusLine status={availability.status} fallback="" />
      <Button id="btn-claim" variant="passkey" disabled={availability.disabled} onClick={claim}>
        <KeyRound className="size-5" aria-hidden="true" /> Create with passkey
      </Button>
      <Note className="mt-2.5 text-[13px]">Keys are generated locally and never leave your device in plain form.</Note>
    </div>
  );
}
