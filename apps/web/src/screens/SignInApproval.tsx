/** Approve a "Sign in with Poweur ID" request (EPIC-008), from app.js. */
import { useRef, useState } from "react";
import { normalizeMatchCode, SIGNIN_MATCH_CODE_DIGITS } from "@poweur/client";
import { CircleCheck } from "lucide-react";
import { approveSignIn, beginSignInApproval } from "../actions/signin";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { toast } from "../state/ui";
import { Button } from "../ui/Button";
import { Chip } from "../ui/Display";
import { Input, Note, Textarea } from "../ui/Field";
import { isShellRuntime } from "../lib/storage.js";
import { SubPage } from "../ui/Layout";

function ErrorLine({ error }: { error: string }) {
  return error ? <p className="compose-status err mt-2 min-h-5 text-sm text-danger">{error}</p> : null;
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
      <Note className="text-[13px]">
        Signing as <strong>{identity || "no identity selected"}</strong>.
      </Note>
      {auth.request.response_uri && (
        <div className="mt-4">
          {auth.context && (
            <p id="auth-context" className="mb-2 text-sm">
              {auth.context}
            </p>
          )}
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
      <ErrorLine error={auth.error || localError} />
      {unlocked ? (
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
          Approve
        </Button>
      ) : (
        <Button id="btn-auth-unlock" variant="passkey" className="mt-4" onClick={() => push("unlock", { returnTo: "auth" })}>
          Unlock to approve
        </Button>
      )}
    </SubPage>
  );
}
