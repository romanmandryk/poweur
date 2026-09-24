/** Share one path, and review / revoke everything shared (EPIC-005), from app.js. */
import { useRef, useState } from "react";
import { grantAllowsWrite, grantExpired } from "@poweur/client";
import { addFileRequest, addPublicLink, addShare, describeAudience, grantsForPath, revokeShare } from "../../actions/files";
import { activeClient, resolveForActive } from "../../actions/relay";
import { AudiencePicker } from "../../components/AudiencePicker";
import { cn } from "../../lib/cn";
import { useData } from "../../state/data";
import { openPanel, toast } from "../../state/ui";
import { Button } from "../../ui/Button";
import { Chip, SectionLabel } from "../../ui/Display";
import { FormGroup, Input, Label } from "../../ui/Field";

export function openSharePanel(path: string) {
  if (!activeClient()) {
    toast("Unlock your identity first", "warning");
    return;
  }
  openPanel(`Share ${path.split("/").pop()}`, (close) => <ShareForm path={path} close={close} />);
}

const PERMISSIONS = [
  { id: "read", label: "Read" },
  { id: "rw", label: "Read and write" },
] as const;

function ShareForm({ path, close }: { path: string; close: () => void }) {
  const contacts = useData((state) => state.contacts.list);
  const grants = useData((state) => state.files.grants);
  const existing = grantsForPath(grants, path);
  const [audience, setAudience] = useState<string[]>([]);
  const [permissions, setPermissions] = useState<"read" | "rw">("read");
	const [mode, setMode] = useState<"people" | "link" | "request">("people");
	const [capabilityUrl, setCapabilityUrl] = useState("");
  const expiry = useRef<HTMLInputElement>(null);
	const password = useRef<HTMLInputElement>(null);
	const maxUploads = useRef<HTMLInputElement>(null);
	const maxBytesMB = useRef<HTMLInputElement>(null);
	const maxObjectMB = useRef<HTMLInputElement>(null);
	const allowedTypes = useRef<HTMLInputElement>(null);
	const notify = useRef<HTMLInputElement>(null);
	const maxDownloads = useRef<HTMLInputElement>(null);

  return (
    <div>
      <p className="mb-1 font-mono text-[13px] text-muted">/{path}</p>
      {existing.length > 0 && <p className="mb-2 text-[13px]">Already shared with {existing.map(describeAudience).join("; ")}.</p>}
      <div className="policy-challenges mb-4 flex flex-wrap gap-2" aria-label="Share kind">
        <button type="button" onClick={() => setMode("people")} className={cn("chip policy-challenge inline-flex min-h-9 items-center rounded-full border border-sep bg-surface-2 px-3 text-xs font-bold", mode === "people" && "selected border-accent bg-accent text-white")}>People</button>
        <button type="button" onClick={() => setMode("link")} className={cn("chip policy-challenge inline-flex min-h-9 items-center rounded-full border border-sep bg-surface-2 px-3 text-xs font-bold", mode === "link" && "selected border-accent bg-accent text-white")}>Public link</button>
        <button type="button" onClick={() => setMode("request")} className={cn("chip policy-challenge inline-flex min-h-9 items-center rounded-full border border-sep bg-surface-2 px-3 text-xs font-bold", mode === "request" && "selected border-accent bg-accent text-white")}>Request files</button>
      </div>
      {mode === "people" ? <>
      <AudiencePicker resolve={resolveForActive} contacts={contacts} groups={[]} onChange={setAudience} />
      <SectionLabel className="mt-4 px-0">They may</SectionLabel>
      <div id="share-perms" className="policy-challenges mb-3 flex flex-wrap gap-2">
        {PERMISSIONS.map((option) => (
          <button
            key={option.id}
            type="button"
            data-perm={option.id}
            onClick={() => setPermissions(option.id)}
            className={cn(
              "chip policy-challenge inline-flex min-h-9 items-center rounded-full border border-sep bg-surface-2 px-3 text-xs font-bold text-fg",
              permissions === option.id && "selected border-accent bg-accent text-white",
            )}
          >
            {option.label}
          </button>
        ))}
      </div>
      </> : <>
        <p className="mb-3 text-[13px] text-muted">{mode === "link" ? "Anyone with the link can browse and download this item. The link never grants write access." : "Anyone with the link can upload a new file here. They cannot see, replace, or delete files."}</p>
        {capabilityUrl ? (
          <FormGroup>
            <Label htmlFor="capability-url">{mode === "link" ? "Public link" : "Upload link"}</Label>
            <div className="flex gap-2">
              <Input id="capability-url" readOnly value={capabilityUrl} />
              <Button variant="secondary" onClick={() => void navigator.clipboard?.writeText(capabilityUrl)}>Copy</Button>
            </div>
          </FormGroup>
        ) : <>
          <FormGroup><Label htmlFor="share-link-password">Password (optional)</Label><Input ref={password} id="share-link-password" type="password" /></FormGroup>
          {mode === "link" ? (
            <FormGroup><Label htmlFor="link-max-downloads">Maximum downloads</Label><Input ref={maxDownloads} id="link-max-downloads" type="number" min="0" placeholder="Unlimited" /></FormGroup>
          ) : <>
          <div className="grid grid-cols-2 gap-3">
            <FormGroup><Label htmlFor="request-max-uploads">Maximum files</Label><Input ref={maxUploads} id="request-max-uploads" type="number" min="0" placeholder="Unlimited" /></FormGroup>
            <FormGroup><Label htmlFor="request-max-bytes">Max MB total</Label><Input ref={maxBytesMB} id="request-max-bytes" type="number" min="0" placeholder="Unlimited" /></FormGroup>
            <FormGroup><Label htmlFor="request-max-object">Max MB per file</Label><Input ref={maxObjectMB} id="request-max-object" type="number" min="0" placeholder="64" /></FormGroup>
          </div>
          <FormGroup><Label htmlFor="request-types">Accepted types (optional)</Label><Input ref={allowedTypes} id="request-types" placeholder="image/*, application/pdf" /></FormGroup>
          <label className="mb-3 flex items-center gap-2 text-[13px]" htmlFor="request-notify">
            <input ref={notify} id="request-notify" type="checkbox" defaultChecked />
            Tell me when a file arrives
          </label>
          </>}
        </>}
      </>}
      <FormGroup>
        <Label htmlFor="share-expiry">Stop working on (optional)</Label>
        <Input ref={expiry} id="share-expiry" type="date" />
      </FormGroup>
      <Button
        id="btn-share-go"
        className="mt-4"
        disabled={(mode === "people" && audience.length === 0) || Boolean(capabilityUrl)}
        onClick={async () => {
          const until = expiry.current?.value || undefined;
          if (mode === "people") {
            close();
            void addShare(path, { audience, permissions, expiry: until });
            return;
          }
          if (mode === "link") {
            const url = await addPublicLink(path, {
              expiry: until,
              password: password.current?.value || undefined,
              maxDownloads: Number(maxDownloads.current?.value || 0) || undefined,
            });
            if (url) setCapabilityUrl(url);
            return;
          }
          const url = await addFileRequest(path, {
            expiry: until,
            password: password.current?.value || undefined,
            maxUploads: Number(maxUploads.current?.value || 0) || undefined,
            maxBytes: (Number(maxBytesMB.current?.value || 0) || 0) * 1024 * 1024 || undefined,
            maxObjectBytes: (Number(maxObjectMB.current?.value || 0) || 0) * 1024 * 1024 || undefined,
            allowedTypes: allowedTypes.current?.value.split(",").map((value) => value.trim()).filter(Boolean),
            notify: Boolean(notify.current?.checked),
          });
          if (url) setCapabilityUrl(url);
        }}
      >
        {mode === "people" ? "Share" : capabilityUrl ? "Created" : mode === "link" ? "Create public link" : "Create upload link"}
      </Button>
    </div>
  );
}

/** Everything we have shared, and the one button that takes it back. */
export function openSharesPanel() {
  if (!activeClient()) {
    toast("Unlock your identity first", "warning");
    return;
  }
  openPanel("Shared by you", () => <SharesList />);
}

function SharesList() {
  const grants = useData((state) => state.files.grants);
  const [revoking, setRevoking] = useState<string | null>(null);

  return (
    <div id="shares-list">
      {grants.length === 0 ? (
        <p className="text-[13px] text-muted">You have not shared anything yet. Open a folder under /shared or /apps and tap the share button.</p>
      ) : (
        grants.map((grant: any) => {
          const writable = grantAllowsWrite(grant);
          const expired = grantExpired(grant);
          return (
            <div key={grant.share_id} className="share-row flex items-center gap-2.5 border-b border-sep py-3 last:border-b-0">
              <div className="share-row-body flex min-w-0 flex-1 flex-col gap-1">
                <div className="share-path font-mono text-[13px] font-semibold [overflow-wrap:anywhere]">/{grant.path}</div>
                <div className="text-[13px] text-muted">{describeAudience(grant)}</div>
                <div className="flex flex-wrap gap-1.5 text-[13px]">
                  <Chip tone={writable ? "warning" : "accent"}>{writable ? "read + write" : "read"}</Chip>
                  {grant.expires_at && <Chip tone={expired ? "danger" : "neutral"}>{expired ? "expired" : `until ${String(grant.expires_at).slice(0, 10)}`}</Chip>}
                </div>
              </div>
              <Button
                size="sm"
                variant="secondary"
                className="min-h-10 shrink-0 text-danger"
                data-revoke={grant.share_id}
                disabled={revoking === grant.share_id}
                onClick={async () => {
                  setRevoking(grant.share_id);
                  if (!(await revokeShare(grant.share_id))) setRevoking(null);
                }}
              >
                Revoke
              </Button>
            </div>
          );
        })
      )}
    </div>
  );
}
