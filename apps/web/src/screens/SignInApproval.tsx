/** Approve a "Sign in with Poweur ID" request (EPIC-008), from app.js. */
import { useRef } from "react";
import { CircleCheck } from "lucide-react";
import { approveSignIn, beginSignInApproval } from "../actions/signin";
import { useData } from "../state/data";
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { toast } from "../state/ui";
import { Button } from "../ui/Button";
import { Chip } from "../ui/Display";
import { Note, Textarea } from "../ui/Field";
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

  if (auth.loading) {
    return (
      <SubPage title="Sign in with Poweur ID">
        <p role="status">Verifying the app’s origin…</p>
      </SubPage>
    );
  }

  if (auth.result) {
    const responseUri: string | undefined = auth.request?.response_uri;
    const redirect = responseUri
      ? `${responseUri}${responseUri.includes("?") ? "&" : "?"}response=${encodeURIComponent(auth.result.encoded)}`
      : "";
    return (
      <SubPage title="Approved">
        <div className="empty-icon flex justify-center text-success">
          <CircleCheck className="size-12" strokeWidth={1.6} aria-hidden="true" />
        </div>
        <h2 className="mb-2 text-center text-xl font-bold">{auth.metadata?.name || auth.request?.domain || "App"}</h2>
        <p className="mb-3 text-muted">
          {auth.result.delivered ? "The signed approval was delivered." : "Copy this one-time response back to the app."}
        </p>
        <Textarea id="auth-response" readOnly rows={5} className="min-h-0" value={auth.result.encoded} />
        <div className="stack mt-4 flex flex-col gap-3">
          <Button
            id="btn-auth-copy"
            variant="ghost"
            onClick={async () => {
              await navigator.clipboard?.writeText(auth.result.encoded);
              toast("Response copied", "success");
            }}
          >
            Copy response
          </Button>
          {redirect && (
            <a className="btn btn-primary block rounded-button bg-accent p-4 text-center text-[17px] font-semibold text-white no-underline" href={redirect}>
              Continue to app
            </a>
          )}
        </div>
      </SubPage>
    );
  }

  if (!auth.request) {
    return (
      <SubPage title="Approve sign-in" onBack={pop}>
        <p className="mb-2 text-[13px] text-muted">
          Paste the code or <code>poweur://auth</code> link shown by the app.
        </p>
        <Textarea ref={pasted} id="auth-request-input" rows={7} className="min-h-0" placeholder="Paste sign-in request" defaultValue={auth.input} />
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
      <ErrorLine error={auth.error} />
      {unlocked ? (
        <Button id="btn-auth-approve" className="mt-4" onClick={() => void approveSignIn()}>
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
