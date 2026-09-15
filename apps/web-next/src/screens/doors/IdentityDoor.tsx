/**
 * The identity host's own door (E15-T9). `bob.poweur.net` is not a place to
 * ask who you are — the URL bar already said. Either Bob signs in, or the name
 * is free and can be claimed, as `bob` and nothing else.
 */
import { useEffect, type ReactNode } from "react";
import { probeDoor, retryDoor } from "../../actions/door";
import { createIdentity, signInWithPasskey } from "../../actions/identity";
import { launcherAppUrl } from "../../lib/claim";
import { domainOf, handleOf } from "../../lib/identity";
import { useData } from "../../state/data";
import { useSession, type ModeInfo } from "../../state/session";
import { Avatar } from "../../ui/Avatar";
import { Button } from "../../ui/Button";
import { Skeleton } from "../../ui/Display";
import { Note } from "../../ui/Field";
import { openJoinDevicePanel } from "../JoinDevice";
import { DoorPage } from "./DoorPage";

function DoorCard({ children }: { children: ReactNode }) {
  return (
    <div className="door-card flex animate-fade-in-up flex-col items-center gap-1.5 rounded-card bg-surface px-5 pt-7 pb-5 text-center shadow-card">
      {children}
    </div>
  );
}

/** The way out of an identity host: the launcher, where a name is chosen. */
function DoorFooter({ info }: { info: ModeInfo }) {
  if (!info.launcherHost) return null;
  return (
    <div className="landing-alt flex justify-center pt-1">
      <a id="door-launcher-link" className="btn-link min-h-11 px-1 py-2.5 text-sm text-accent hover:underline" href={launcherAppUrl(info.launcherHost)}>
        Claim a different name
      </a>
    </div>
  );
}

/** The identity host's own name, claimed as itself and nothing else. */
function claimThisHost(info: ModeInfo) {
  if (!info.handle || !info.domain) return;
  void createIdentity({ handle: info.handle, domain: info.domain, hosted: (info.hostedDomains ?? []).includes(info.domain) });
}

export function IdentityDoor() {
  const info = useSession((state) => state.mode);
  const identity = useSession((state) => state.identity);
  const door = useData((state) => state.door);
  const subject = info.subject ?? "";

  useEffect(() => {
    probeDoor(info, identity);
  }, [info, identity, door.state]);

  const name = <div className="door-name text-[22px] font-bold tracking-[-.3px] break-words">{handleOf(subject)}</div>;
  const domain = <div className="door-domain -mt-1 text-[15px] text-muted">{domainOf(subject)}</div>;
  const primary = "door-primary mt-4 w-full";
  const secondary = "door-secondary mt-2.5 w-full";

  if (door.state === "idle" || door.state === "checking") {
    return (
      <DoorPage id="door">
        <DoorCard>
          <Avatar identity={subject} size="xl" />
          <div className="door-name text-[22px] font-bold">{subject}</div>
          <div className="skeleton-stack my-4 flex w-full flex-col gap-2.5" aria-hidden="true">
            <Skeleton className="h-3.5 w-3/5 self-center" />
            <Skeleton className="h-13 w-full rounded-button" />
          </div>
          <p role="status" className="text-[13px] text-muted">
            Checking this name…
          </p>
        </DoorCard>
      </DoorPage>
    );
  }

  if (door.state === "claimed") {
    return (
      <DoorPage id="door">
        <DoorCard>
          <Avatar identity={subject} size="xl" />
          {name}
          {domain}
          <Button id="btn-door-signin" variant="passkey" className={primary} onClick={() => void signInWithPasskey(subject)}>
            🔑 Sign in with passkey
          </Button>
          <Button
            id="opt-join-device"
            variant="ghost"
            className={secondary}
            data-join-identity={subject}
            onClick={() => openJoinDevicePanel(subject)}
          >
            📱 Add this device
          </Button>
          <Note className="mt-3 text-[13px]">
            This name is taken. If it is yours, your passkey opens it — on this device or a device you already use.
          </Note>
        </DoorCard>
        <DoorFooter info={info} />
      </DoorPage>
    );
  }

  if (door.state === "claimable") {
    return (
      <DoorPage id="door">
        <DoorCard>
          <Avatar identity={subject} size="xl" />
          <div className="door-kicker text-xs font-bold tracking-[.6px] text-success uppercase">This name is free</div>
          {name}
          {domain}
          <Button id="btn-door-claim" className={primary} onClick={() => claimThisHost(info)}>
            Claim {subject}
          </Button>
          <Note className="mt-3 text-[13px]">
            You are claiming <strong>{subject}</strong> — the name this page is served from. Your keys are generated here and never leave in
            plain form.
          </Note>
        </DoorCard>
        <DoorFooter info={info} />
      </DoorPage>
    );
  }

  if (door.state === "offline") {
    return (
      <DoorPage id="door">
        <DoorCard>
          <Avatar identity={subject} size="xl" />
          <div className="door-name text-[22px] font-bold">{subject}</div>
          <p className="door-message my-2 text-[15px] leading-normal text-muted">Can't reach the relay, so this name can't be checked.</p>
          <Button id="btn-door-retry" variant="ghost" className={primary} onClick={retryDoor}>
            Try again
          </Button>
          <Button id="btn-door-signin" variant="passkey" className={secondary} onClick={() => void signInWithPasskey(subject)}>
            🔑 Sign in with passkey
          </Button>
        </DoorCard>
      </DoorPage>
    );
  }

  // Unavailable — reserved, blocked, or a policy refusal. Offering a claim here
  // would spend a passkey on a name registration is going to refuse.
  return (
    <DoorPage id="door">
      <DoorCard>
        <Avatar identity={subject} size="xl" />
        <div className="door-name text-[22px] font-bold">{subject}</div>
        <p className="door-message my-2 text-[15px] leading-normal text-muted">{door.message || "This name is not available."}</p>
        <DoorFooter info={info} />
      </DoorCard>
    </DoorPage>
  );
}
