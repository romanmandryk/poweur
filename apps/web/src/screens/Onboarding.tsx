/**
 * First run, three skippable steps (E15-T5). The one question worth
 * interrupting for is the inbox policy: its default is a decision the user
 * never knowingly made. Continue saves a step's answers first; a step whose
 * save fails stays put with the error beside the field.
 */
import { useRef, useState } from "react";
import { PartyPopper } from "lucide-react";
import { savePolicy } from "../actions/account";
import { PolicyControls, type PolicyControlsHandle } from "../components/PolicyControls";
import { ProfileEditor, type ProfileEditorHandle } from "../components/ProfileEditor";
import { cn } from "../lib/cn";
import { handleOf } from "../lib/identity";
import { ONBOARDING_MODES } from "../lib/policy";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { Button } from "../ui/Button";
import { SubPage } from "../ui/Layout";

const STEPS = 3;

export function finishOnboarding() {
  useData.setState({ onboard: null });
  useRoute.setState({ page: "messages", sub: null, params: {} });
}

export function Onboarding() {
  const identity = useSession((state) => state.identity) ?? "";
  const unlocked = useSession((state) => state.unlocked);
  const step: number = useData((state) => state.onboard?.step ?? 1);
  const policy = useData((state) => state.policy);
  const profile = useData((state) => state.profile);
  const policyControls = useRef<PolicyControlsHandle>(null);
  const profileEditor = useRef<ProfileEditorHandle>(null);
  const [busy, setBusy] = useState(false);

  const goTo = (next: number) => useData.setState({ onboard: { step: next } });

  const next = async () => {
    if (step >= STEPS) {
      finishOnboarding();
      return;
    }
    setBusy(true);
    try {
      let saved = true;
      if (step === 1 && policyControls.current) saved = await policyControls.current.save();
      // Nothing typed is nothing to write: an empty profile document would say
      // "this person set a profile" when they skipped past it.
      if (step === 2 && profileEditor.current?.hasInput()) saved = await profileEditor.current.save();
      if (saved) goTo(step + 1);
    } finally {
      setBusy(false);
    }
  };

  return (
    <SubPage
      title={`Set up ${handleOf(identity)}`}
      actions={
        <button
          id="btn-onboard-skip"
          type="button"
          aria-label="Skip setup"
          onClick={finishOnboarding}
          className="btn-back onboard-skip ml-auto px-3 text-[15px] font-semibold text-accent"
        >
          Skip
        </button>
      }
      footer={
        <div className="onboard-footer flex gap-2">
          {step > 1 && (
            <Button id="btn-onboard-back" variant="secondary" className="min-h-11 flex-1" onClick={() => goTo(Math.max(1, step - 1))}>
              Back
            </Button>
          )}
          <Button id="btn-onboard-next" className="min-h-11 flex-1" disabled={busy} onClick={() => void next()}>
            {step === STEPS ? "Start using Poweur" : "Continue"}
          </Button>
        </div>
      }
    >
      <div
        className="onboard-progress mb-5 flex gap-1.5"
        role="progressbar"
        aria-valuemin={1}
        aria-valuemax={STEPS}
        aria-valuenow={step}
      >
        {Array.from({ length: STEPS }, (_, index) => (
          <span key={index} className={cn("onboard-dot h-1 flex-1 rounded-full bg-surface-3", index < step && "done bg-accent")} />
        ))}
      </div>

      {step === 1 && (
        <>
          <h2 className="onboard-title mb-1.5 text-[22px] font-bold">Who can message you?</h2>
          <p className="text-[13px] text-muted">You can change this any time in Settings.</p>
          {unlocked && (
            // Recommended, not imposed: Skip leaves the relay's default in place.
            <PolicyControls
              ref={policyControls}
              // Start open: the first message (from a friend, or the hello.poweur.net
              // demo) must arrive without a detour through Requests. The strict mode
              // is not offered to a new ID at all.
              policy={policy.explicit && policy.doc ? policy.doc : { version: 1, mode: "open" }}
              modes={ONBOARDING_MODES}
              explicit={policy.explicit}
              showSave={false}
              onSave={savePolicy}
            />
          )}
        </>
      )}

      {step === 2 && (
        <>
          <h2 className="onboard-title mb-1.5 text-[22px] font-bold">How should people see you?</h2>
          <p className="mb-4 text-[13px] text-muted">Optional, and public — this is what anyone who looks you up reads.</p>
          {unlocked && <ProfileEditor ref={profileEditor} profile={profile.doc} showSave={false} />}
        </>
      )}

      {step === 3 && (
        <div className="onboard-done py-6 text-center">
          <div className="empty-state-icon mx-auto mb-3 flex size-16 items-center justify-center rounded-full bg-accent-soft text-accent">
            <PartyPopper className="size-8" strokeWidth={1.75} aria-hidden="true" />
          </div>
          <h2 className="onboard-title mb-1.5 text-[22px] font-bold">You're set</h2>
          <p className="text-muted">
            {identity} can send and receive encrypted messages, keep files, and share them. Add someone from Contacts to get started.
          </p>
        </div>
      )}
    </SubPage>
  );
}
