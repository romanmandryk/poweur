import { useEffect, useRef, useState } from "react";
import { UserPlus } from "lucide-react";
import type { IdentifyData, Launcher, Page } from "../lib/page";
import { Button } from "../ui/Button";
import { Card, Monogram } from "../ui/Display";
import { ErrorLine, Input } from "../ui/Field";
import { Shell } from "../ui/Shell";
import { useCreateID } from "./useCreateID";

export function Identify({ page }: { page: Page<IdentifyData> }) {
  const d = page.data;
  const appName = d.client?.name;
  const idRef = useRef<HTMLInputElement>(null);
  const [ready, setReady] = useState("");

  return (
    <Shell page={page} narrow>
      <Card className="animate-fade-in-up">
        <div className="mb-6 flex flex-col items-center text-center">
          {appName ? <Monogram text={appName} className="mb-4" /> : null}
          <h1 id="identify-title" className="text-[24px] leading-tight font-extrabold tracking-[-.3px]">
            {appName ? `Sign in to ${appName}` : "Sign in"}
          </h1>
          <p className="mt-1 text-[15px] text-muted">with your Poweur ID</p>
        </div>
        <form method="post" action={`/t/${d.txn}/identify`}>
          <label htmlFor="identity" className="sr-only">
            Poweur ID
          </label>
          <Input
            ref={idRef}
            id="identity"
            name="identity"
            type="text"
            inputMode="url"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            required
            autoFocus
            placeholder={`yourname.${d.launcher?.domain ?? "poweur.net"}`}
            defaultValue={d.hint}
            className="text-center text-[17px]"
          />
          <ErrorLine className="text-center">{d.error}</ErrorLine>
          {ready && (
            <p id="created-ready" role="status" className="mt-3 rounded-control bg-success/12 px-3 py-2 text-center text-sm font-semibold text-success">
              {ready}
            </p>
          )}
          <Button type="submit" id="identify-continue" className="mt-4">
            Continue
          </Button>
        </form>
        {d.launcher && (
          <CreateID
            launcher={d.launcher}
            txn={d.txn}
            onReady={(id) => {
              if (idRef.current) idRef.current.value = id;
              setReady(`${id} is ready — continue to sign in.`);
              idRef.current?.focus();
            }}
          />
        )}
      </Card>
      <form method="post" action={`/t/${d.txn}/cancel`} className="mt-4 text-center">
        <button type="submit" id="identify-cancel" className="text-sm font-medium text-muted hover:text-fg">
          Cancel{appName ? ` and go back to ${appName}` : ""}
        </button>
      </form>
    </Shell>
  );
}

/**
 * "New to Poweur?": pick a name, check it at the launcher, create it in a new
 * tab. This tab notices when the name is taken — by this visitor — and fills
 * it in.
 */
function CreateID({ launcher, txn, onReady }: { launcher: Launcher; txn: string; onReady: (id: string) => void }) {
  const [open, setOpen] = useState(false);
  const create = useCreateID(launcher, txn, onReady);

  useEffect(() => {
    if (open) document.getElementById("new-handle")?.focus();
  }, [open]);

  if (!open) {
    return (
      <div className="mt-6 border-t border-sep pt-5 text-center">
        <button type="button" id="create-open" onClick={() => setOpen(true)} className="inline-flex items-center gap-2 text-[15px] font-semibold text-accent">
          <UserPlus className="size-4" aria-hidden="true" /> New to Poweur? Create an ID
        </button>
      </div>
    );
  }
  return (
    <div id="create-id-section" className="mt-6 animate-fade-in-up border-t border-sep pt-5">
      <h2 className="text-[17px] font-bold">Create a Poweur ID</h2>
      <p className="mt-1 mb-4 text-sm text-muted">A name you own. Your device holds its key — no password.</p>
      <label htmlFor="new-handle" className="sr-only">
        Choose a name
      </label>
      <div className="flex items-stretch overflow-hidden rounded-control border-[1.5px] border-sep bg-surface focus-within:border-accent focus-within:shadow-[0_0_0_3px_var(--color-accent-soft)]">
        <input
          id="new-handle"
          type="text"
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder="yourname"
          aria-describedby="new-handle-status"
          value={create.input}
          onChange={(e) => create.setInput(e.currentTarget.value)}
          className="min-w-0 flex-1 bg-transparent px-4 py-3 text-base text-fg outline-none placeholder:text-faint"
        />
        <span className="flex items-center pr-4 text-base text-muted">.{launcher.domain}</span>
      </div>
      <p id="new-handle-status" role="status" className={`mt-2 min-h-5 text-sm ${create.tone === "ok" ? "font-semibold text-success" : create.tone === "bad" ? "text-danger" : "text-muted"}`}>
        {create.status}
      </p>
      <a
        id="create-id"
        href={create.href}
        target="_blank"
        rel="noopener"
        onClick={create.onOpen}
        className="mt-2 flex w-full items-center justify-center rounded-button border-[1.5px] border-accent bg-surface px-4 py-3 text-base font-semibold text-accent no-underline"
      >
        {create.handle && create.available ? `Create ${create.handle}.${launcher.domain}` : `Create at ${launcher.host}`}
      </a>
      <p className="mt-2 text-center text-[13px] text-muted">Opens a new tab. Come back here when you're done.</p>
    </div>
  );
}
