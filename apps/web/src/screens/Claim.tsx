/**
 * Claiming from somewhere that is not a launcher host: "Add new ID" inside the
 * app (E15-T10).
 */
import { useRoute } from "../state/route";
import { useSession } from "../state/session";
import { SubPage } from "../ui/Layout";
import { ClaimCard } from "./doors/ClaimCard";

export function Claim() {
  const info = useSession((state) => state.mode);
  const pop = useRoute((state) => state.pop);
  return (
    <SubPage title="New identity" onBack={pop}>
      <ClaimCard info={info} />
    </SubPage>
  );
}
