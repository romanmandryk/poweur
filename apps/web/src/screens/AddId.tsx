/**
 * Add an identity to this device. What it may ask depends on the door it was
 * opened from (E15-T9): on `bob.poweur.net` the subject is in the URL bar, so
 * no typed identity; in the shell, no passkey (a browser authenticator).
 */
import { useRef, type ReactNode } from "react";
import { ChevronRight, KeyRound, Smartphone, Sparkles, type LucideIcon } from "lucide-react";
import { signInWithPasskey } from "../actions/identity";
import { cn } from "../lib/cn";
import { addIdOptions } from "../lib/mode.js";
import { isShellRuntime } from "../lib/storage.js";
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { Button } from "../ui/Button";
import { inputClass } from "../ui/Field";
import { SubPage } from "../ui/Layout";
import { RelayPrompt } from "./doors/RelayPrompt";
import { relayPromptVisible } from "./doors/RelayPrompt";
import { openJoinDevicePanel } from "./JoinDevice";

const CARD = "option-card relative flex min-h-13 w-full items-center gap-4 rounded-card bg-surface px-4 py-[18px] text-left";

function OptionIcon({ icon: Icon }: { icon: LucideIcon }) {
  return (
    <div className="option-icon-wrap flex size-12 shrink-0 items-center justify-center rounded-xl bg-accent-soft text-accent">
      <Icon className="size-6" strokeWidth={1.9} aria-hidden="true" />
    </div>
  );
}

function OptionBody({ title, description }: { title: ReactNode; description: ReactNode }) {
  return (
    <div className="option-body flex-1">
      <div className="option-title text-base font-semibold text-fg">{title}</div>
      <div className="option-desc mt-0.5 text-sm text-muted">{description}</div>
    </div>
  );
}

function OptionButton({ id, icon, title, description, onClick, ...rest }: {
  id: string;
  icon: LucideIcon;
  title: ReactNode;
  description: ReactNode;
  onClick: () => void;
  "data-join-identity"?: string;
}) {
  return (
    <button
      id={id}
      type="button"
      onClick={onClick}
      className={cn(CARD, "transition-[background-color,transform] active:scale-[.99] active:bg-surface-2 [@media(hover:hover)]:hover:bg-surface-2")}
      {...rest}
    >
      <OptionIcon icon={icon} />
      <OptionBody title={title} description={description} />
      <ChevronRight className="option-arrow size-5 shrink-0 text-faint" aria-hidden="true" />
    </button>
  );
}

export function AddId() {
  const info = useSession((state) => state.mode);
  useSession((state) => state.config);
  const pop = useRoute((state) => state.pop);
  const push = useRoute((state) => state.push);
  const typed = useRef<HTMLInputElement>(null);
  // mode.js types `mode` as its AppMode union; the store holds the same value as a string.
  const options = addIdOptions(info as Parameters<typeof addIdOptions>[0]);
  const subject = options.joinSubject;
  const here = isShellRuntime() ? "device" : "browser";
  const signIn = () => void signInWithPasskey(typed.current?.value ?? "");

  return (
    <SubPage title={subject ? "Sign in" : "Add identity"} onBack={pop}>
      <p className="mb-5 text-[13px] text-muted">
        {subject ? (
          <>
            Sign in to <strong>{subject}</strong> on this {here}.
          </>
        ) : options.create ? (
          "Connect or create a Poweur ID identity on this device."
        ) : (
          "Sign in with the passkey this browser already has for your identity."
        )}
      </p>
      {relayPromptVisible() && <RelayPrompt />}
      <div className="option-list flex flex-col gap-3">
        {options.passkey && !subject && (
          <div className={cn(CARD, "option-card-form-wrap flex-col items-stretch")}>
            <div className="option-card-top flex w-full items-center gap-4">
              <OptionIcon icon={KeyRound} />
              <OptionBody title="Sign in with existing passkey" description="Enter your identity to authenticate in this browser" />
            </div>
            <div className="option-inline-form mt-3.5 flex items-center gap-2.5">
              <input
                ref={typed}
                id="signin-id-input"
                type="text"
                placeholder="alice.poweur.net"
                autoCapitalize="none"
                autoCorrect="off"
                autoComplete="off"
                spellCheck={false}
                inputMode="url"
                className={cn(inputClass, "w-auto min-w-0 flex-1")}
                onKeyDown={(event) => {
                  if (event.key === "Enter") signIn();
                }}
              />
              <Button id="btn-signin-passkey" className="w-auto shrink-0 px-5 py-3.5" onClick={signIn}>
                Sign in
              </Button>
            </div>
          </div>
        )}

        {options.passkey && subject && (
          <OptionButton
            id="btn-door-signin"
            icon={KeyRound}
            title="Sign in with passkey"
            description={`Unlock ${subject} in this browser`}
            onClick={() => void signInWithPasskey(subject)}
          />
        )}

        {options.join && (
          <OptionButton
            id="opt-join-device"
            icon={Smartphone}
            title={`Add this ${here} to an existing ID`}
            description={
              subject ? "Show a code, approve it on a device you already use" : "Enter your ID, show a code, approve it on a device you already use"
            }
            data-join-identity={subject || undefined}
            onClick={() => openJoinDevicePanel(subject)}
          />
        )}

        {options.create && (
          <OptionButton
            id="opt-create-new"
            icon={Sparkles}
            title="Add new ID"
            description={isShellRuntime() ? "Create a fresh identity on this device" : "Create a fresh identity with a passkey"}
            onClick={() => push("claim")}
          />
        )}
      </div>
    </SubPage>
  );
}
