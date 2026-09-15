/**
 * "Key changed" (EPIC-007): a contact's resolved key no longer matches the one
 * pinned, and no signed rotation covers it. Resolves true only when the user
 * explicitly trusts the new key; closing the panel is "don't send".
 */
import { fingerprintOrKey } from "@poweur/client";
import { openPanel } from "../state/ui";
import { Button } from "../ui/Button";

export function confirmKeyChange({
  recipient,
  pinnedKey,
  resolvedKey,
}: {
  recipient: string;
  pinnedKey?: string | null;
  resolvedKey?: string | null;
}): Promise<boolean> {
  return new Promise((resolve) => {
    let trusted = false;
    openPanel(
      "Key changed",
      (close) => (
        <div>
          <p className="val-warn mb-2 font-semibold text-warning">{recipient}'s key does not match the one you pinned.</p>
          <p className="mb-3 text-[13px] text-muted">
            No rotation statement covers this change. It can mean a compromised relay or registrar impersonating your contact. Verify
            with them out of band before you trust it.
          </p>
          <KeyRow label="Pinned" id="km-pinned" value={pinnedKey ? fingerprintOrKey(pinnedKey) : ""} />
          <KeyRow label="Now" id="km-resolved" value={resolvedKey ? fingerprintOrKey(resolvedKey) : ""} />
          <p className="mt-2 text-[13px] text-muted">
            These are safety numbers — read them to {recipient} over a channel you already trust. They match on both sides when nothing
            has been tampered with.
          </p>
          <div className="panel-actions mt-4 flex flex-wrap gap-2">
            <Button id="km-cancel" className="min-h-11 flex-[1_1_40%] px-4 py-3 text-[15px]" onClick={close}>
              Don't send
            </Button>
            <Button
              id="km-trust"
              variant="danger"
              className="min-h-11 flex-[1_1_40%] px-4 py-3 text-[15px]"
              onClick={() => {
                trusted = true;
                close();
              }}
            >
              Trust new key
            </Button>
          </div>
        </div>
      ),
      () => resolve(trusted),
    );
  });
}

function KeyRow({ label, id, value }: { label: string; id: string; value: string }) {
  return (
    <div className="kv-row flex items-start gap-3 border-b border-sep py-2.5 text-sm last:border-b-0">
      <span className="kv-label w-[90px] shrink-0 pt-px font-medium text-muted">{label}</span>
      <span id={id} className="kv-value flex-1 font-mono text-[13px] break-all text-fg">
        {value}
      </span>
    </div>
  );
}
