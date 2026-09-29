/**
 * The parent-domain landing (E15-T8), also the shell's front door. One
 * dominant control: a name, with the domain as a fixed suffix inside the field.
 * It wears poweur.org's look: the glass P on its glow, the night ground, and
 * the site's closing "claim your name" box as the claim itself.
 */
import { useRef, useState } from "react";
import { ArrowUpRight, KeyRound, Lock, Tag, type LucideIcon } from "lucide-react";
import { goToIdentityDoor, lookUpExistingId, type ExistingIdVerdict } from "../../actions/door";
import { cn } from "../../lib/cn";
import { LEGAL_LINKS, showsPoweurLegal } from "../../lib/legal";
import { hasRelayUrl } from "../../lib/storage.js";
import { pendingChatTarget } from "../../lib/visit";
import { useRoute } from "../../state/route";
import { useSession, type ModeInfo } from "../../state/session";
import { BrandMark } from "../../ui/Logo";
import { ClaimCard } from "./ClaimCard";
import { cardClass, DoorPage, LinkButton } from "./DoorPage";
import { RelayPrompt, relayPromptVisible } from "./RelayPrompt";

/** Where running your own relay under your own domain is explained. */
export const SELF_HOSTING_DOCS_URL = "https://poweur.org/docs/relay/self-hosting";

/** The three sentences. Any longer and nobody reads them. */
const HOW_IT_WORKS: [LucideIcon, string, string][] = [
  [Tag, "A name you own", "Your ID is an address on the internet — like a domain, but for a person."],
  [Lock, "Messages and files under it", "End-to-end encrypted mail and a synced drive, addressed to the name."],
  [KeyRound, "Sign in with it", "No passwords to reuse: your device holds the key, and apps ask it."],
];

export function Landing() {
  const info = useSession((state) => state.mode);
  useSession((state) => state.config); // a newly chosen relay changes what shows
  const push = useRoute((state) => state.push);
  const prompt = relayPromptVisible();
  // Production is the shell's default relay, so the picker is a switch, not a
  // gate; with no relay at all there is nothing to claim under.
  const showClaim = !prompt || hasRelayUrl();
  const chatTarget = info.mode === "launcher" ? pendingChatTarget() : "";

  return (
    <DoorPage id="landing">
      <div className="landing-hero flex flex-col items-center pt-4 text-center landscape:max-h-[500px]:pt-0">
        <BrandMark className="landing-icon mb-6 landscape:max-h-[500px]:hidden" height={84} />
        <div className="landing-eyebrow mb-3 inline-flex items-center gap-2 font-mono text-[12px] font-medium tracking-[.08em] text-violet-600 uppercase dark:text-violet-300">
          <span aria-hidden="true" className="size-1.5 rounded-full bg-violet-400 shadow-[0_0_12px_var(--color-violet-400)]" />
          One open ID
        </div>
        <h1 className="landing-title animate-fade-in-up text-brand-gradient text-[34px] leading-[1.06] font-semibold tracking-[-1px] text-balance landscape:max-h-[500px]:text-[24px]">
          Your name. Your inbox. Your files.
        </h1>
        <p className="landing-sub mt-3 max-w-[360px] animate-fade-in-up text-base leading-normal text-muted">
          Claim an identity you own, and take it everywhere.
        </p>
        {chatTarget && (
          <p id="landing-chat-target" className="mt-3 max-w-[360px] rounded-full bg-violet-500/10 px-4 py-1.5 text-sm text-violet-700 dark:text-violet-200">
            Claim your ID, or open the one you have, to message <span className="font-mono">{chatTarget}</span>.
          </p>
        )}
      </div>
      {prompt && (
        <div className={`landing-card ${cardClass}`}>
          <RelayPrompt className="mb-0 p-0" />
        </div>
      )}
      {showClaim && <ClaimCard hero info={info} />}
      {info.mode === "launcher" ? (
        <ExistingId info={info} />
      ) : (
        <div className="landing-alt flex justify-center">
          <LinkButton id="opt-have-id" onClick={() => push("add-id")}>
            I already have an ID
          </LinkButton>
        </div>
      )}
      <ol className={cn("landing-steps flex list-none flex-col gap-4", cardClass, "landscape:max-h-[500px]:hidden")}>
        {HOW_IT_WORKS.map(([Icon, title, body]) => (
          <li key={title} className="landing-step flex items-start gap-3.5">
            <span
              aria-hidden="true"
              className="landing-step-icon flex size-9 shrink-0 items-center justify-center rounded-[10px] border border-violet-300/40 bg-violet-500/10 text-violet-600 dark:text-violet-300"
            >
              <Icon className="size-[18px]" strokeWidth={1.9} />
            </span>
            <div>
              <div className="landing-step-title text-[15px] font-semibold">{title}</div>
              <div className="landing-step-body text-sm leading-[1.45] text-muted">{body}</div>
            </div>
          </li>
        ))}
      </ol>
      <div className="landing-alt flex flex-col items-center gap-1">
        <a
          id="opt-own-domain"
          href={SELF_HOSTING_DOCS_URL}
          target="_blank"
          rel="noopener"
          className="btn-link inline-flex min-h-11 items-center gap-1 px-1 py-2.5 text-sm text-muted hover:text-fg hover:underline"
        >
          Host your own relay and domain
          <ArrowUpRight className="size-3.5" aria-hidden="true" />
        </a>
        {showsPoweurLegal(info.hostedDomains) && <LegalLinks />}
      </div>
    </DoorPage>
  );
}

/** Privacy and terms for the hosted service, at the foot of its front door. */
function LegalLinks() {
  const link = "px-1 py-2 text-[13px] text-faint hover:text-muted hover:underline";
  return (
    <nav aria-label="Legal" className="landing-legal flex items-center gap-1.5">
      <a id="link-privacy" href={LEGAL_LINKS.privacy} target="_blank" rel="noopener" className={link}>
        Privacy
      </a>
      <span aria-hidden="true" className="text-faint">
        ·
      </span>
      <a id="link-terms" href={LEGAL_LINKS.terms} target="_blank" rel="noopener" className={link}>
        Terms
      </a>
      <span aria-hidden="true" className="text-faint">
        ·
      </span>
      <a id="link-legal" href={LEGAL_LINKS.legal} target="_blank" rel="noopener" className={link}>
        Contact
      </a>
    </nav>
  );
}

/**
 * "I already have an ID" on the launcher: nothing to sign in to here, because
 * a hosted name's keys live on its own origin. Check the name exists and go to
 * its door; say so plainly when it doesn't.
 */
function ExistingId({ info }: { info: ModeInfo }) {
  const field = useRef<HTMLInputElement>(null);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<Exclude<ExistingIdVerdict, { state: "found" }> | null>(null);

  const open = async () => {
    if (checking) return;
    setChecking(true);
    setError(null);
    const verdict = await lookUpExistingId(field.current?.value ?? "", info);
    if (verdict.state === "found") {
      goToIdentityDoor(verdict.identity, verdict.url);
      return;
    }
    setChecking(false);
    setError(verdict);
    field.current?.focus();
  };

  return (
    <div id="have-id" className={cn("have-id", cardClass)}>
      <label htmlFor="have-id-input" className="have-id-label mb-1 block text-[15px] font-semibold">
        Already have an ID?
      </label>
      <p className="mb-3 text-[13px] text-muted">We'll take you to its own page to sign in.</p>
      <div className="flex items-stretch gap-2">
        <input
          ref={field}
          id="have-id-input"
          type="text"
          placeholder={info.domain ? `yourname.${info.domain}` : "yourname.example.com"}
          autoCapitalize="none"
          autoCorrect="off"
          autoComplete="username"
          spellCheck={false}
          inputMode="url"
          enterKeyHint="go"
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? "have-id-error" : undefined}
          onInput={() => error && setError(null)}
          onKeyDown={(event) => {
            if (event.key === "Enter") void open();
          }}
          className={cn(
            "have-id-input min-w-0 flex-1 rounded-control border-[1.5px] border-transparent bg-surface-2 px-3.5 py-3 font-mono text-[15px] text-fg outline-none transition-colors placeholder:text-faint focus:border-accent dark:bg-white/6",
            error && "border-warning/60",
          )}
        />
        <button
          id="btn-have-id"
          type="button"
          disabled={checking}
          onClick={() => void open()}
          className="btn btn-have-id shrink-0 rounded-control bg-fg px-5 text-[15px] font-semibold text-bg transition-opacity disabled:opacity-50 [@media(hover:hover)]:hover:opacity-90"
        >
          {checking ? "Checking…" : "Open"}
        </button>
      </div>
      {error && (
        <p id="have-id-error" role="alert" data-state={error.state} className="have-id-error mt-2.5 text-[13px] text-warning">
          {error.message}
        </p>
      )}
    </div>
  );
}
