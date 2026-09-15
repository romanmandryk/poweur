/** Share one path, and review / revoke everything shared (EPIC-005), from app.js. */
import { useRef, useState } from "react";
import { grantAllowsWrite, grantExpired } from "@poweur/client";
import { addShare, describeAudience, grantsForPath, revokeShare } from "../../actions/files";
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
  const expiry = useRef<HTMLInputElement>(null);

  return (
    <div>
      <p className="mb-1 font-mono text-[13px] text-muted">/{path}</p>
      {existing.length > 0 && <p className="mb-2 text-[13px]">Already shared with {existing.map(describeAudience).join("; ")}.</p>}
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
      <FormGroup>
        <Label htmlFor="share-expiry">Stop working on (optional)</Label>
        <Input ref={expiry} id="share-expiry" type="date" />
      </FormGroup>
      <Button
        id="btn-share-go"
        className="mt-4"
        disabled={audience.length === 0}
        onClick={() => {
          const until = expiry.current?.value || undefined;
          close();
          void addShare(path, { audience, permissions, expiry: until });
        }}
      >
        Share
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
