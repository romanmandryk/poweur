import { useEffect } from "react";
import { unlock } from "../actions/identity";
import { CUSTODY_COPY, custodyOf } from "../lib/custody";
import { domainOf, handleOf } from "../lib/identity";
import { loadIdentityRecord } from "../lib/storage.js";
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { Avatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Note } from "../ui/Field";
import { SubPage } from "../ui/Layout";

export function Unlock() {
  const identity = useSession((state) => state.identity);
  const pop = useRoute((state) => state.pop);
  const record = identity ? loadIdentityRecord(identity) : null;
  const missing = !identity || !record;

  // Nothing to unlock on this device: offer to add one instead.
  useEffect(() => {
    if (missing) useRoute.getState().push("add-id");
  }, [missing]);

  if (missing) return null;
  const copy = CUSTODY_COPY[custodyOf(record)];

  return (
    <SubPage title="Unlock" onBack={pop} bodyClassName="flex min-h-[60dvh] flex-col items-center justify-center gap-4 text-center">
      <Avatar identity={identity} size="xl" className="unlock-avatar animate-pop-in shadow-[0_8px_32px_rgb(0_0_0/.2)]" />
      <div>
        <div className="unlock-name text-[22px] font-bold">{handleOf(identity)}</div>
        <div className="unlock-sub text-[15px] text-muted">{domainOf(identity)}</div>
      </div>
      <Button id="btn-do-unlock" variant="passkey" className="max-w-70" onClick={() => void unlock()}>
        🔓 {copy.action}
      </Button>
      <Note className="mt-0 max-w-[220px]">{copy.note}</Note>
    </SubPage>
  );
}
