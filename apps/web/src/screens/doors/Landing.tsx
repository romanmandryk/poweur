/**
 * The parent-domain landing (E15-T8), also the shell's front door. One
 * dominant control: a name, with the domain as a fixed suffix inside the field.
 */
import { KeyRound, Lock, Tag, type LucideIcon } from "lucide-react";
import { hasRelayUrl } from "../../lib/storage.js";
import { useRoute } from "../../state/route";
import { useSession } from "../../state/session";
import { BrandTile } from "../../ui/Logo";
import { ClaimCard } from "./ClaimCard";
import { cardClass, DoorPage, LinkButton } from "./DoorPage";
import { RelayPrompt, relayPromptVisible } from "./RelayPrompt";

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

  return (
    <DoorPage id="landing">
      <div className="landing-hero flex flex-col items-center pt-2 text-center">
        <BrandTile className="landing-icon mb-5 size-20 rounded-[22px] landscape:max-h-[500px]:hidden" />
        <h1 className="landing-title animate-fade-in-up text-[30px] leading-tight font-extrabold tracking-[-.7px] landscape:max-h-[500px]:text-[22px]">
          Your name. Your inbox. Your files.
        </h1>
        <p className="landing-sub mt-2.5 animate-fade-in-up text-base leading-normal text-muted">
          Claim an identity you own, and take it everywhere.
        </p>
      </div>
      {prompt && (
        <div className={`landing-card ${cardClass}`}>
          <RelayPrompt className="mb-0 p-0" />
        </div>
      )}
      {showClaim && <ClaimCard info={info} />}
      <ol className="landing-steps flex animate-fade-in-up list-none flex-col gap-4 landscape:max-h-[500px]:hidden">
        {HOW_IT_WORKS.map(([Icon, title, body]) => (
          <li key={title} className="landing-step flex items-start gap-3.5">
            <span aria-hidden="true" className="landing-step-icon flex size-9 shrink-0 items-center justify-center rounded-[10px] bg-accent-soft text-accent">
              <Icon className="size-5" strokeWidth={1.9} />
            </span>
            <div>
              <div className="landing-step-title text-[15px] font-[650]">{title}</div>
              <div className="landing-step-body text-sm leading-[1.45] text-muted">{body}</div>
            </div>
          </li>
        ))}
      </ol>
      <div className="landing-alt flex flex-wrap items-center justify-center gap-2.5 pt-1">
        <LinkButton id="opt-have-id" onClick={() => push("add-id")}>
          I already have an ID
        </LinkButton>
        <span aria-hidden="true" className="landing-alt-sep text-faint">
          ·
        </span>
        <LinkButton id="opt-own-domain" onClick={() => push("claim", { dns: true })}>
          Use my own domain
        </LinkButton>
      </div>
    </DoorPage>
  );
}
