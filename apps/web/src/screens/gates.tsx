import { LockOpen } from "lucide-react";
import { CUSTODY_COPY, custodyOf } from "../lib/custody";
import { domainOf, handleOf } from "../lib/identity";
import { loadIdentityRecord } from "../lib/storage.js";
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { Avatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Note } from "../ui/Field";
import { BrandMark } from "../ui/Logo";

/** The honest fallback: an unrecognised host, so no claim is offered. */
export function Welcome() {
  const push = useRoute((state) => state.push);
  return (
    <div className="welcome-wrap flex min-h-[70vh] flex-col items-center justify-center gap-4 p-8 text-center landscape:max-h-[500px]:min-h-0">
      <BrandMark className="welcome-icon mb-4" />
      <h1 className="welcome-title animate-fade-in-up text-[28px] font-extrabold tracking-[-.5px]">Welcome to Poweur ID</h1>
      <p className="welcome-sub max-w-[260px] animate-fade-in-up text-base leading-normal text-muted">
        Encrypted, identity-first messaging. No phone number required.
      </p>
      <div className="welcome-cta mt-2 animate-fade-in-up">
        <Button id="btn-welcome-start" className="w-auto px-10" onClick={() => push("add-id")}>
          Get started
        </Button>
      </div>
    </div>
  );
}

/** Shown on any destination that needs keys, so unlocking is one tap from anywhere. */
export function Locked() {
  const identity = useSession((state) => state.identity) ?? "";
  const push = useRoute((state) => state.push);
  const copy = CUSTODY_COPY[custodyOf(loadIdentityRecord(identity))];
  return (
    <div className="unlock-wrap flex min-h-full flex-col items-center justify-center gap-5 p-8 text-center">
      <Avatar identity={identity} size="xl" className="unlock-avatar animate-pop-in shadow-[0_8px_32px_rgb(0_0_0/.2)]" />
      <div>
        <div className="unlock-name text-[22px] font-bold">{handleOf(identity)}</div>
        <div className="unlock-sub text-[15px] text-muted">{domainOf(identity)}</div>
      </div>
      <Button id="btn-unlock-main" variant="passkey" className="mt-4 max-w-80" onClick={() => push("unlock")}>
        <LockOpen className="size-5" aria-hidden="true" /> {copy.action}
      </Button>
      <Note className="mt-0 max-w-[220px]">{copy.note}</Note>
    </div>
  );
}
