/** Approve a "Sign in with Poweur" request (EPIC-008, EPIC-028). */
import { useEffect, useRef, useState } from "react";
import { signInRequestUri } from "@poweur/client";
import { CircleCheck, LockOpen, Send, Smartphone } from "lucide-react";
import { unlock } from "../actions/identity";
import { approveSignIn, sendSignInToApp } from "../actions/signin";
import { androidIntentFor, appLinkFor, approveDevice, approvePageLink } from "../lib/approve";
import { CUSTODY_COPY, custodyOf } from "../lib/custody";
import { cn } from "../lib/cn";
import { identityAppUrl, joinIdentityFor } from "../lib/claim";
import { handleOf, isValidIdentity } from "../lib/identity";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { switchIdentity, useSession } from "../state/session";
import { toast } from "../state/ui";
import { Button, buttonClass } from "../ui/Button";
import { QRCode } from "../ui/QRCode";
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

const TITLE = "Sign in with Poweur";

const PUSH_NOTICES: Record<string, string> = {
  sent: "Sent. Open your Poweur app from the home screen to approve it.",
  "too-soon": "Already sent — give it a few seconds.",
  "too-many": "Sent as many times as this sign-in allows.",
  paused: "Sending is paused for a few minutes. Approve on the screen where you started.",
  closed: "This sign-in is no longer waiting.",
  failed: "It could not be sent to your app.",
};

/**
 * This browser holds no Poweur ID, so approval happens elsewhere (EPIC-028).
 * The device decides what leads: Android hands the request to the store app
 * (or comes straight back if it is not installed), iOS offers the app and a
 * push to the home-screen app — Safari cannot open an installed web app —
 * and a desktop shows the same link as a QR for the phone.
 */
function ApproveElsewhere() {
  const auth = useData((state) => state.auth);
  const push = useRoute((state) => state.push);
  const [device] = useState(() => approveDevice());
  const [notice, setNotice] = useState("");
  const requestLink = signInRequestUri(auth.input) ?? "";
  const pageLink = requestLink ? approvePageLink(requestLink) : "";
  const intent = device === "android" && requestLink ? androidIntentFor(requestLink, pageLink) : "";
  const tried = useRef(false);
  useEffect(() => {
    if (!intent || auth.noApp || tried.current) return;
    tried.current = true;
    globalThis.location.assign(intent);
  }, [intent, auth.noApp]);

  if (device === "desktop") {
    return (
      <div id="auth-elsewhere" className="mt-5">
        <h3 className="text-[17px] font-bold">Approve on your phone</h3>
        <p className="mt-1 text-sm text-muted">Your keys aren't in this browser. Scan with your phone's camera.</p>
        {pageLink && <QRCode value={pageLink} label="QR code to approve on your phone" className="mx-auto mt-4 w-48" />}
        <Button id="btn-auth-add" variant="ghost" className="mt-4" onClick={() => push("add-id")}>
          Add your Poweur ID to this browser
        </Button>
      </div>
    );
  }
  const appLink = device === "android" ? intent : appLinkFor(auth.input);
  return (
    <div id="auth-elsewhere" className="mt-5 flex flex-col gap-2.5">
      <p className="text-sm text-muted">Your keys aren't in this browser.</p>
      {appLink && (
        <a id="btn-auth-open-app" href={appLink} className={buttonClass("primary")}>
          <Smartphone className="size-5" aria-hidden="true" /> Open in Poweur app
        </a>
      )}
      {auth.pushUri && (
        <>
          <Button
            id="btn-auth-send-app"
            variant="ghost"
            onClick={async () => setNotice(PUSH_NOTICES[await sendSignInToApp()] ?? PUSH_NOTICES.failed)}
          >
            <Send className="size-4" aria-hidden="true" /> Send to my home-screen app
          </Button>
          {notice && (
            <p id="auth-send-notice" role="status" className="text-center text-[13px] text-muted">
              {notice}
            </p>
          )}
        </>
      )}
      <Button id="btn-auth-add" variant="ghost" onClick={() => push("add-id")}>
        Use this browser instead
      </Button>
    </div>
  );
}

/** A pushed sign-in: tap the number the starting screen shows. One try. */
function PickNumber({ choices, onPick, disabled }: { choices: string[]; onPick: (n: string) => void; disabled: boolean }) {
  return (
    <div id="auth-pick" className="mt-5">
      <p className="text-[15px] font-semibold">Tap the number shown where you started</p>
      <p className="mt-1 text-[13px] text-muted">If you don't see a number there, you didn't start this sign-in — close this.</p>
      <div className="mt-3 grid grid-cols-3 gap-2.5">
        {choices.map((n) => (
          <button
            key={n}
            type="button"
            disabled={disabled}
            className="auth-pick-choice rounded-control border-[1.5px] border-sep bg-surface py-4 font-mono text-[28px] font-extrabold tracking-[.08em] hover:border-accent disabled:opacity-50"
            onClick={() => onPick(n)}
          >
            {n}
          </button>
        ))}
      </div>
    </div>
  );
}

export function SignInApproval() {
  const auth = useData((state) => state.auth);
  const identity = useSession((state) => state.identity);
  const unlocked = useSession((state) => state.unlocked);
  const pop = useRoute((state) => state.pop);
  const push = useRoute((state) => state.push);
  const record = identity ? loadIdentityRecord(identity) : null;
  const mode: any = useSession((state) => state.mode);
  const unlockAction = CUSTODY_COPY[custodyOf(record)].action;

  if (auth.loading && !auth.request) {
    return (
      <SubPage title={TITLE}>
        <p role="status">Verifying the app’s origin…</p>
      </SubPage>
    );
  }

  if (auth.result) {
    const { delivered, resumeUri } = auth.result;
    return (
      <SubPage title={TITLE}>
        <div className="empty-icon flex justify-center text-success">
          <CircleCheck className="size-12" strokeWidth={1.6} aria-hidden="true" />
        </div>
        <h2 className="mb-2 text-center text-xl font-bold">{auth.metadata?.name || auth.request?.domain || "App"}</h2>
        {resumeUri && !auth.crossDevice ? (
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
            <p id="auth-result-note" className="mb-1 text-muted">
              This app can't receive approvals directly. Copy this approval and paste it into the app you're signing in to.
            </p>
            <p className="mb-3 text-[13px] text-danger">
              Never paste it anywhere else — anyone holding it can sign in as you there for the next few minutes.
            </p>
            <Textarea id="auth-response" readOnly rows={5} className="min-h-0" value={auth.result.encoded} />
            <Button
              id="btn-auth-copy"
              variant="ghost"
              className="mt-4"
              onClick={async () => {
                await navigator.clipboard?.writeText(auth.result.encoded);
                toast("Approval copied", "success");
              }}
            >
              Copy approval
            </Button>
          </>
        )}
      </SubPage>
    );
  }

  if (!auth.request) {
    return (
      <SubPage title={TITLE} onBack={pop}>
        <p className="text-muted">Open a sign-in link, or scan the QR code on the screen where you're signing in, to approve it here.</p>
        <ErrorLine error={auth.error} />
      </SubPage>
    );
  }

  const scopes: string[] = auth.scopes ?? [];
  const launcher = mode?.mode === "launcher";
  const hasIdentities = (listIdentities() as string[]).length > 0;
  const elsewhere = !launcher && !hasIdentities && !isShellRuntime();
  return (
    <SubPage title={TITLE} onBack={pop}>
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
      {auth.crossDevice && (
        <p id="auth-warning" className="mt-2 text-[13px] text-muted">
          Only approve a sign-in you just started yourself.
        </p>
      )}
      {elsewhere ? (
        <ApproveElsewhere />
      ) : (
        <>
          {launcher ? <OtherIdentity primary /> : <IdentityPicker />}
          {!launcher && <OtherIdentity />}
        </>
      )}
      <ErrorLine error={auth.error} />
      {elsewhere || launcher ? null : unlocked && identity ? (
        auth.choices && auth.request.response_uri ? (
          <PickNumber choices={auth.choices} disabled={auth.loading || Boolean(auth.error)} onPick={(n) => void approveSignIn(n)} />
        ) : (
          <Button id="btn-auth-approve" className="mt-4" disabled={auth.loading} onClick={() => void approveSignIn()}>
            Approve as {handleOf(identity)}
          </Button>
        )
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
