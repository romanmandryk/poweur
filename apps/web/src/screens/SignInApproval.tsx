/** Approve a "Sign in with Poweur ID" request (EPIC-008), from app.js. */
import { useRef, useState } from "react";
import { normalizeMatchCode, SIGNIN_MATCH_CODE_DIGITS } from "@poweur/client";
import { CircleCheck, LockOpen } from "lucide-react";
import { unlock } from "../actions/identity";
import { approveSignIn, beginSignInApproval } from "../actions/signin";
import { CUSTODY_COPY, custodyOf } from "../lib/custody";
import { cn } from "../lib/cn";
import { identityAppUrl, joinIdentityFor } from "../lib/claim";
import { handleOf, isValidIdentity } from "../lib/identity";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { switchIdentity, useSession } from "../state/session";
import { toast } from "../state/ui";
import { Button } from "../ui/Button";
import { Chip } from "../ui/Display";
import { Avatar } from "../ui/Avatar";
import { Input, inputClass, Note, Textarea } from "../ui/Field";
import { isShellRuntime, listIdentities, loadIdentityRecord } from "../lib/storage.js";
import { SubPage } from "../ui/Layout";

function ErrorLine({ error }: { error: string }) {
  return error ? <p className="compose-status err mt-2 min-h-5 text-sm text-danger">{error}</p> : null;
}

/** Which of this browser's IDs signs in. Switching locks the previous one. */
function IdentityPicker() {
  const identity = useSession((state) => state.identity);
  const identities: string[] = listIdentities();
  if (!identities.length) {
    return <p className="mt-4 text-sm text-muted">There is no Poweur ID on this device yet.</p>;
  }
  return (
    <div className="mt-4">
      <label htmlFor="auth-identity" className="mb-1.5 block text-sm font-semibold">
        Sign in as
      </label>
      <div className="flex items-center gap-3">
        <Avatar identity={identity || identities[0]} size="md" />
        {identities.length > 1 ? (
          <select
            id="auth-identity"
            className={cn(inputClass, "min-w-0 flex-1")}
            value={identity ?? ""}
            onChange={(e) => switchIdentity(e.currentTarget.value)}
          >
            {!identity && <option value="" disabled>Choose an ID</option>}
            {identities.map((id) => (
              <option key={id} value={id}>
                {id}
              </option>
            ))}
          </select>
        ) : (
          <div id="auth-identity" className="min-w-0 flex-1 truncate text-[15px] font-semibold">
            {identities[0]}
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * An ID that is not in this browser's list — another host's, a self-hosted
 * one, one made at its own address. Keys live per origin, so the way to use it
 * is its own app with this same request; one already here is just switched to.
 */
function OtherIdentity({ primary = false }: { primary?: boolean }) {
  const mode = useSession((state) => state.mode);
  const input = useData((state) => state.auth.input);
  const typed = useRef<HTMLInputElement>(null);
  const [error, setError] = useState("");
  if (isShellRuntime()) return null;

  const go = () => {
    const id = joinIdentityFor(typed.current?.value ?? "", mode as any);
    if (!isValidIdentity(id)) {
      setError("Enter an ID like alice.poweur.net");
      return;
    }
    if ((listIdentities() as string[]).includes(id)) {
      setError("");
      switchIdentity(id);
      return;
    }
    globalThis.location.assign(`${identityAppUrl(id)}?auth=${encodeURIComponent(input)}`);
  };

  const fields = (
    <>
      <div className="mt-2 flex items-center gap-2.5">
        <Input
          ref={typed}
          id="auth-other-input"
          type="text"
          placeholder="alice.poweur.net"
          autoCapitalize="none"
          autoCorrect="off"
          autoComplete="off"
          spellCheck={false}
          inputMode="url"
          className="min-w-0 flex-1"
          onKeyDown={(e) => {
            if (e.key === "Enter") go();
          }}
        />
        <Button id="btn-auth-other" variant="ghost" className="w-auto shrink-0 px-5" onClick={go}>
          Continue
        </Button>
      </div>
      <ErrorLine error={error} />
    </>
  );
  if (primary) {
    return (
      <div id="auth-other" className="mt-4">
        <label htmlFor="auth-other-input" className="block text-sm font-semibold">
          Which Poweur ID are you signing in with?
        </label>
        <p className="mt-1 text-[13px] text-muted">Your keys live on your ID's own address. We'll take you there to approve.</p>
        {fields}
      </div>
    );
  }
  return (
    <details id="auth-other" className="mt-3">
      <summary className="cursor-pointer text-sm font-semibold text-accent">Sign in as a different ID</summary>
      {fields}
    </details>
  );
}

export function SignInApproval() {
  const auth = useData((state) => state.auth);
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);
  const pop = useRoute((state) => state.pop);
  const push = useRoute((state) => state.push);
  const pasted = useRef<HTMLTextAreaElement>(null);
  const matchRef = useRef<HTMLInputElement>(null);
  const [match, setMatch] = useState("");
  const [localError, setLocalError] = useState("");
  // In the shell the app being signed in to is never in this browser, so a
  // same-device finish is impossible and the code is required.
  const shell = isShellRuntime() || auth.requireCode;
  const record = identity ? loadIdentityRecord(identity) : null;
  const mode: any = useSession((state) => state.mode);
  const unlockAction = CUSTODY_COPY[custodyOf(record)].action;

  if (auth.loading) {
    return (
      <SubPage title="Sign in with Poweur ID">
        <p role="status">Verifying the app’s origin…</p>
      </SubPage>
    );
  }

  if (auth.result) {
    const { delivered, resumeUri } = auth.result;
    return (
      <SubPage title="Approved">
        <div className="empty-icon flex justify-center text-success">
          <CircleCheck className="size-12" strokeWidth={1.6} aria-hidden="true" />
        </div>
        <h2 className="mb-2 text-center text-xl font-bold">{auth.metadata?.name || auth.request?.domain || "App"}</h2>
        {resumeUri ? (
          <>
            <p id="auth-result-note" className="mb-3 text-muted">Approved. Taking you back to the app…</p>
            <a
              id="btn-auth-continue"
              className="btn btn-primary block rounded-button bg-accent p-4 text-center text-[17px] font-semibold text-white no-underline"
              href={resumeUri}
              rel="noreferrer"
            >
              Continue to app
            </a>
          </>
        ) : delivered ? (
          <p id="auth-result-note" className="mb-3 text-muted">
            Approved. Go back to the screen where you started signing in — it will continue by itself.
          </p>
        ) : (
          <>
            <p id="auth-result-note" className="mb-3 text-muted">Copy this one-time response back to the app.</p>
            <Textarea id="auth-response" readOnly rows={5} className="min-h-0" value={auth.result.encoded} />
            <Button
              id="btn-auth-copy"
              variant="ghost"
              className="mt-4"
              onClick={async () => {
                await navigator.clipboard?.writeText(auth.result.encoded);
                toast("Response copied", "success");
              }}
            >
              Copy response
            </Button>
          </>
        )}
      </SubPage>
    );
  }

  if (!auth.request) {
    return (
      <SubPage title="Approve sign-in" onBack={pop}>
        <p className="mb-2 text-[13px] text-muted">
          Paste the sign-in code shown by the app.
        </p>
        <Textarea key={auth.input} ref={pasted} id="auth-request-input" rows={7} className="min-h-0" placeholder="Paste sign-in code" defaultValue={auth.input} />
        <ErrorLine error={auth.error} />
        <Button id="btn-auth-load" className="mt-4" onClick={() => void beginSignInApproval(pasted.current?.value ?? "")}>
          Check request
        </Button>
      </SubPage>
    );
  }

  const scopes: string[] = auth.scopes ?? [];
  const launcher = mode?.mode === "launcher";
  return (
    <SubPage title="Approve sign-in" onBack={pop}>
      <div className="settings-id-card flex flex-col items-center gap-2.5 px-5 pt-8 pb-5 text-center">
        <div className="settings-id-name text-xl font-bold">{auth.metadata?.name}</div>
        <div className="settings-id-domain text-sm text-muted">{auth.request.audience}</div>
        <Chip tone="success">Origin verified</Chip>
      </div>
      <h2 className="mb-2 text-xl font-bold">{auth.headline}</h2>
      {auth.request.statement && <p className="mb-3">{auth.request.statement}</p>}
      {scopes.length ? (
        <div className="settings-group mb-2">
          <div className="settings-group-label pb-1.5 text-[13px] font-semibold tracking-[.05em] text-muted uppercase">Permissions</div>
          <ul className="list-disc pl-5">
            {scopes.map((scope) => (
              <li key={scope}>{scope}</li>
            ))}
          </ul>
        </div>
      ) : (
        <p className="text-muted">This app requests sign-in only, with no home access.</p>
      )}
      {auth.request.response_uri && auth.context && (
        <p id="auth-context" className="mt-4 text-sm">
          {auth.context}
        </p>
      )}
      {launcher ? <OtherIdentity primary /> : <IdentityPicker />}
      {!launcher && <OtherIdentity />}
      <ErrorLine error={auth.error || localError} />
      {launcher ? null : unlocked && identity ? (
        <>
          {auth.request.response_uri && (
            <div className="mt-4">
              <label htmlFor="auth-match" className="mb-1 block text-sm font-semibold">
                {shell ? "Code shown by the app you are signing in to" : "Started on another screen? Enter the code it shows"}
              </label>
              <Input
                ref={matchRef}
                id="auth-match"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={8}
                placeholder={shell ? "Required" : "Leave empty if you started in this browser"}
                onChange={(e) => setMatch(e.currentTarget.value)}
              />
              <p className="mt-1 text-[13px] text-muted">
                Only enter a code that is on a screen in front of you. If someone sent you this link or a code, cancel.
              </p>
            </div>
          )}
          <Button
            id="btn-auth-approve"
            className="mt-4"
            onClick={() => {
              const code = normalizeMatchCode(match);
              if (auth.request.response_uri && (shell || code) && code.length !== SIGNIN_MATCH_CODE_DIGITS) {
                setLocalError(`Enter the ${SIGNIN_MATCH_CODE_DIGITS}-digit code from the screen where you started.`);
                matchRef.current?.focus();
                return;
              }
              setLocalError("");
              void approveSignIn(code);
            }}
          >
            Approve as {handleOf(identity)}
          </Button>
        </>
      ) : identity ? (
        <>
          <Button id="btn-auth-unlock" variant="passkey" className="mt-4" onClick={() => void unlock({ inPlace: true })}>
            <LockOpen className="size-5" aria-hidden="true" /> {unlockAction}
          </Button>
          <Note className="mt-2">{CUSTODY_COPY[custodyOf(record)].note}. Then approve right here.</Note>
        </>
      ) : (
        <Button id="btn-auth-add" variant="ghost" className="mt-4" onClick={() => push("add-id")}>
          Add a Poweur ID to this device
        </Button>
      )}
    </SubPage>
  );
}
