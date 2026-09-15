/**
 * Claiming from somewhere that is not a launcher host: "Add new ID" inside the
 * app, or the self-hosted DNS path (`params.dns`) — a different intent, not a
 * checkbox (E15-T10).
 */
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { SubPage } from "../ui/Layout";
import { ClaimCard, DnsClaimCard } from "./doors/ClaimCard";

export function Claim() {
  const info = useSession((state) => state.mode);
  const dns = useRoute((state) => Boolean(state.params.dns));
  const pop = useRoute((state) => state.pop);
  return (
    <SubPage title={dns ? "Use my own domain" : "New identity"} onBack={pop}>
      {dns ? <DnsClaimCard info={info} /> : <ClaimCard info={info} />}
    </SubPage>
  );
}
